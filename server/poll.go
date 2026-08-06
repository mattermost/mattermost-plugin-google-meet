// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/pluginapi/cluster"

	"github.com/mattermost/mattermost-plugin-google-meet/server/store/kvstore"
)

// conferenceRejoinGrace delays marking a calendar-bound meeting post as ended until this long
// after the last known record end, so a brief drop-and-rejoin within the same calendar event
// instance doesn't flip the post to "ended" and back.
const conferenceRejoinGrace = 1 * time.Minute

// eventBindingRetention is added to a calendar instance's scheduled end when computing an
// EventPostBinding's ExpiresAt, giving artifacts (which can post minutes after the meeting ends)
// room to still find the binding before it is pruned.
const eventBindingRetention = 24 * time.Hour

// startPoller launches the background polling goroutine.
// It is safe to call from OnActivate; the goroutine is stopped via stopPoller.
func (p *Plugin) startPoller() {
	if !p.getConfiguration().EnableConferenceArtifactPosts {
		p.API.LogInfo("Google Meet poller not started: EnableConferenceArtifactPosts is disabled")
		return
	}

	// Defensive: ensure any prior goroutine is stopped before starting a new one
	// so back-to-back startPoller calls (or a missed stopPoller) don't leak.
	p.stopPoller()

	intervalSec := p.getConfiguration().pollInterval()
	p.API.LogInfo("Starting Google Meet poller", "interval_seconds", intervalSec)

	// Capture the channel locally so the goroutine selects on its own channel
	// even if p.pollerStop is later reassigned by another startPoller call.
	stop := make(chan struct{})
	p.pollerStop = stop
	go func() {
		interval := time.Duration(intervalSec) * time.Second
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				p.runPollCycle()
			}
		}
	}()
}

// stopPoller signals the polling goroutine to exit.
func (p *Plugin) stopPoller() {
	if p.pollerStop != nil {
		close(p.pollerStop)
		p.pollerStop = nil
	}
}

// runPollCycle is the work done on each tick. It acquires a distributed mutex
// so that only one node in an HA cluster processes subscriptions at a time.
// The EnableConferenceArtifactPosts guard is duplicated here (the goroutine in
// startPoller is already gated) to keep any future caller from bypassing it.
func (p *Plugin) runPollCycle() {
	if !p.getConfiguration().EnableConferenceArtifactPosts {
		return
	}

	mutex, err := cluster.NewMutex(p.API, "com.mattermost.google-meet.poll")
	if err != nil {
		p.API.LogError("Failed to create polling mutex", "error", err.Error())
		return
	}
	mutex.Lock()
	defer mutex.Unlock()

	store := p.getKVStore()
	if store == nil {
		p.API.LogWarn("Skipping poll cycle: KV store not initialized (plugin not fully configured)")
		return
	}

	spaceIDs, err := store.ListAllSubscriptionSpaceIDs()
	if err != nil {
		p.API.LogError("Failed to list subscription space IDs during poll", "error", err.Error())
		return
	}

	for _, spaceID := range spaceIDs {
		sub, err := store.GetSubscription(spaceID)
		if err != nil {
			p.API.LogWarn("Failed to load subscription during poll", "space_id", spaceID, "error", err.Error())
			continue
		}
		if sub == nil {
			p.API.LogWarn("Subscription index entry has no stored record", "space_id", spaceID)
			continue
		}
		p.pollSubscription(store, sub)
	}

	p.pollAdHocMeetings(store)
}

// pollSubscription handles one subscription: finds new conferences and checks active ones for artifacts.
func (p *Plugin) pollSubscription(store kvstore.KVStore, sub *kvstore.Subscription) {
	// Defense in depth: bail early if the admin disabled the feature mid-cycle
	// so we don't create new conference-started posts after the kill switch.
	if !p.getConfiguration().EnableConferenceArtifactPosts {
		return
	}

	token, err := p.getValidToken(sub.CreatedBy)
	if err != nil {
		p.API.LogWarn("Skipping subscription poll: token lookup failed", "space_id", sub.SpaceID, "created_by", sub.CreatedBy, "error", err.Error())
		return
	}
	if token == nil {
		p.API.LogDebug("Skipping subscription poll: user is not connected to Google", "space_id", sub.SpaceID, "created_by", sub.CreatedBy)
		return
	}

	subChanged := p.processDueScheduledAnnouncements(store, sub, token)
	if pruneExpiredEventBindings(sub) {
		subChanged = true
	}

	records, err := p.listConferenceRecords(token, sub.SpaceID, sub.LastSeenConferenceStart)
	if err != nil {
		p.API.LogWarn("Failed to list conference records", "space_id", sub.SpaceID, "error", err.Error())
		records = nil
	}

	endTimes := make(map[string]*time.Time, len(records))
	for i := range records {
		endTimes[records[i].Name] = records[i].EndTime
	}

	// Buffer the high-water mark advance until the entire batch succeeds.
	// Advancing eagerly per-record would skip past a failed record whenever a
	// later (newer) record in the same batch succeeded — the failed one is
	// older than the new LastSeen and would never be re-fetched. Successful
	// records still get added to ActiveConferenceIDs so their artifacts are
	// polled even on partial-batch failure.
	hadFailure := false
	candidateLastSeen := sub.LastSeenConferenceStart

	for i := range records {
		record := &records[i]
		state, err := store.GetConferencePostState(record.Name)
		if err != nil {
			p.API.LogWarn("Failed to get conference post state", "conference", record.Name, "error", err.Error())
			hadFailure = true
			continue
		}

		if state == nil {
			deferred, err := p.classifyNewConference(store, sub, token, record, endTimes)
			if err != nil {
				p.API.LogWarn("Failed to classify new conference", "conference", record.Name, "error", err.Error())
				hadFailure = true
				continue
			}
			subChanged = true
			if deferred {
				// Nothing posted yet: advance the watermark so this record isn't re-fetched,
				// but skip ActiveConferenceIDs tracking until it either posts or is dropped.
				if record.StartTime != nil && record.StartTime.After(candidateLastSeen) {
					candidateLastSeen = *record.StartTime
				}
				continue
			}
			state, err = store.GetConferencePostState(record.Name)
			if err != nil || state == nil {
				p.API.LogWarn("Conference post state missing right after classification", "conference", record.Name, "error", err)
				hadFailure = true
				continue
			}
		}

		if record.StartTime != nil && record.StartTime.After(candidateLastSeen) {
			candidateLastSeen = *record.StartTime
		}
		if !state.Suppressed && !slices.Contains(sub.ActiveConferenceIDs, record.Name) {
			sub.ActiveConferenceIDs = append(sub.ActiveConferenceIDs, record.Name)
			subChanged = true
		}
	}

	if !hadFailure && candidateLastSeen.After(sub.LastSeenConferenceStart) {
		sub.LastSeenConferenceStart = candidateLastSeen
		subChanged = true
	}

	for i := range records {
		if records[i].EndTime != nil && records[i].EndTime.After(sub.LastConferenceEndTime) {
			sub.LastConferenceEndTime = *records[i].EndTime
			subChanged = true
		}
	}

	if subChanged {
		if err := store.StoreSubscription(sub); err != nil {
			p.API.LogWarn("Failed to update subscription state", "space_id", sub.SpaceID, "error", err.Error())
		}
	}
	subChanged = false

	// Pass 1: resolve (or fetch) the end time of every tracked conference so binding-level
	// end decisions in pass 2 can see all of a calendar instance's records at once, not just
	// the one currently being visited.
	resolvedEnd := make(map[string]*time.Time, len(sub.ActiveConferenceIDs))
	for _, confName := range sub.ActiveConferenceIDs {
		state, _ := store.GetConferencePostState(confName)
		if state != nil && state.Suppressed {
			continue
		}
		endTime := endTimes[confName]
		if endTime == nil && state != nil && !state.MeetingEndedPosted {
			if rec, fetchErr := p.getConferenceRecord(token, confName); fetchErr != nil {
				p.API.LogWarn("Failed to fetch conference record for end-time check", "conference", confName, "error", fetchErr.Error())
			} else if rec != nil {
				endTime = rec.EndTime
			}
		}
		resolvedEnd[confName] = endTime
		if endTime != nil && endTime.After(sub.LastConferenceEndTime) {
			sub.LastConferenceEndTime = *endTime
			subChanged = true
		}
	}

	// Pass 2: apply end-of-meeting decisions and poll artifacts.
	stillActive := sub.ActiveConferenceIDs[:0]
	for _, confName := range sub.ActiveConferenceIDs {
		state, _ := store.GetConferencePostState(confName)
		if state != nil && state.Suppressed {
			// Defensive: suppressed conferences are never appended to ActiveConferenceIDs above,
			// but skip them here too in case one was added before this guard existed.
			continue
		}

		if state != nil && p.maybeMarkConferenceEnded(sub, state, confName, resolvedEnd) {
			if err := store.StoreConferencePostState(confName, state); err != nil {
				p.API.LogWarn("Failed to persist meeting-ended state", "conference", confName, "error", err.Error())
			}
			subChanged = true
		}

		if done := p.pollConferenceArtifacts(store, token, confName); !done {
			stillActive = append(stillActive, confName)
		}
	}
	if len(stillActive) != len(sub.ActiveConferenceIDs) || subChanged {
		sub.ActiveConferenceIDs = stillActive
		if err := store.StoreSubscription(sub); err != nil {
			p.API.LogWarn("Failed to persist pruned subscription state", "space_id", sub.SpaceID, "error", err.Error())
		}
	}
}

// classifyNewConference decides how a conference record with no existing ConferencePostState
// should be handled: bound to an already-announced calendar event instance, deferred until that
// instance's scheduled start, posted immediately, or handled by the legacy cooldown heuristic
// when calendar sync is disabled, errors out, or finds no matching instance. It persists whatever
// ConferencePostState the decision implies, except when deferred=true, in which case there is
// nothing to persist yet — see processDueScheduledAnnouncements.
func (p *Plugin) classifyNewConference(store kvstore.KVStore, sub *kvstore.Subscription, token *kvstore.OAuth2Token, record *conferenceRecord, endTimes map[string]*time.Time) (deferred bool, err error) {
	if p.getConfiguration().EnableCalendarScheduleSync && record.StartTime != nil {
		instance, calErr := p.findScheduledInstance(token, sub.MeetingCode, *record.StartTime)
		switch {
		case calErr != nil && errors.Is(calErr, ErrInsufficientScopes):
			p.API.LogWarn("Calendar schedule sync skipped: token missing calendar scope", "space_id", sub.SpaceID, "conference", record.Name, "error", calErr.Error())
			p.notifyCalendarReconnectNeeded(store, sub.CreatedBy)
		case calErr != nil:
			p.API.LogWarn("Calendar schedule lookup failed; falling back to cooldown heuristic", "space_id", sub.SpaceID, "conference", record.Name, "error", calErr.Error())
		case instance != nil:
			return p.classifyAgainstCalendarInstance(store, sub, record, instance)
		}
	}
	return false, p.classifyWithCooldown(store, sub, record, endTimes)
}

// classifyAgainstCalendarInstance handles a conference record known to belong to calendar
// event instance. If the instance was already announced, the record is bound to that post
// instead of creating a duplicate. Otherwise it is posted now (if the scheduled start has
// already passed) or deferred until then.
func (p *Plugin) classifyAgainstCalendarInstance(store kvstore.KVStore, sub *kvstore.Subscription, record *conferenceRecord, instance *calendarInstance) (bool, error) {
	if idx := findEventPostBindingIndex(sub.EventPostBindings, instance.InstanceID); idx >= 0 {
		binding := &sub.EventPostBindings[idx]
		state := &kvstore.ConferencePostState{
			MeetingPostID: binding.MeetingPostID,
			ThreadRootID:  binding.MeetingPostID,
			ChannelID:     sub.ChannelID,
		}
		if err := store.StoreConferencePostState(record.Name, state); err != nil {
			return false, fmt.Errorf("failed to bind conference to existing calendar post: %w", err)
		}
		if !slices.Contains(binding.ConferenceNames, record.Name) {
			binding.ConferenceNames = append(binding.ConferenceNames, record.Name)
		}
		return false, nil
	}

	if time.Now().Before(instance.Start) {
		sub.ScheduledAnnouncements = append(sub.ScheduledAnnouncements, kvstore.ScheduledAnnouncement{
			ConferenceName:  record.Name,
			EventInstanceID: instance.InstanceID,
			EventSummary:    instance.Summary,
			EventEnd:        instance.End,
			DueAt:           instance.Start,
		})
		return true, nil
	}

	return false, p.postAndBindToCalendarInstance(store, sub, record, instance)
}

// postAndBindToCalendarInstance creates the conference-started post for a calendar-matched
// conference and records the EventPostBinding so later records in the same instance reuse it.
func (p *Plugin) postAndBindToCalendarInstance(store kvstore.KVStore, sub *kvstore.Subscription, record *conferenceRecord, instance *calendarInstance) error {
	postID, err := p.postConferenceStarted(sub, record, instance)
	if err != nil {
		return fmt.Errorf("failed to post conference started: %w", err)
	}
	state := &kvstore.ConferencePostState{
		MeetingPostID: postID,
		ThreadRootID:  postID,
		ChannelID:     sub.ChannelID,
	}
	if err := store.StoreConferencePostState(record.Name, state); err != nil {
		return fmt.Errorf("failed to store conference post state: %w", err)
	}
	p.API.LogInfo("Posted calendar-anchored Google Meet conference notification", "conference", record.Name, "space_id", sub.SpaceID, "channel_id", sub.ChannelID, "meeting_post_id", postID)

	sub.EventPostBindings = append(sub.EventPostBindings, kvstore.EventPostBinding{
		EventInstanceID: instance.InstanceID,
		MeetingPostID:   postID,
		ConferenceNames: []string{record.Name},
		ScheduledEnd:    instance.End,
		ExpiresAt:       instance.End.Add(eventBindingRetention),
	})
	return nil
}

// classifyWithCooldown is the legacy path used when calendar sync is disabled, errors, or has
// no matching event: suppress a rejoin that starts too soon after the previous conference ended
// on the same space, otherwise post immediately.
func (p *Plugin) classifyWithCooldown(store kvstore.KVStore, sub *kvstore.Subscription, record *conferenceRecord, endTimes map[string]*time.Time) error {
	cooldown := p.getConfiguration().conferenceStartCooldown()

	if cooldown > 0 && record.StartTime != nil {
		anchor := latestConferenceEndBefore(endTimes, record.Name, *record.StartTime, sub.LastConferenceEndTime)
		if !anchor.IsZero() && record.StartTime.Sub(anchor) < cooldown {
			p.API.LogInfo("Suppressing Google Meet conference notification within cooldown window", "conference", record.Name, "space_id", sub.SpaceID, "gap", record.StartTime.Sub(anchor).String())
			state := &kvstore.ConferencePostState{
				ChannelID:  sub.ChannelID,
				Suppressed: true,
			}
			return store.StoreConferencePostState(record.Name, state)
		}
	}

	postID, err := p.postConferenceStarted(sub, record, nil)
	if err != nil {
		return fmt.Errorf("failed to post conference started: %w", err)
	}
	p.API.LogInfo("Posted new Google Meet conference notification", "conference", record.Name, "space_id", sub.SpaceID, "channel_id", sub.ChannelID, "meeting_post_id", postID)
	state := &kvstore.ConferencePostState{
		MeetingPostID: postID,
		ThreadRootID:  postID,
		ChannelID:     sub.ChannelID,
	}
	return store.StoreConferencePostState(record.Name, state)
}

// processDueScheduledAnnouncements posts (or drops) conference-started announcements that were
// deferred until their calendar event's scheduled start and whose due time has now passed.
// A deferred conference whose record already ended before the due time is dropped rather than
// posted — most likely someone opened the link early and left before the meeting was due to
// start; if the real meeting still happens, Google Meet will hand back a fresh conference record
// that gets classified (and posted) on its own.
func (p *Plugin) processDueScheduledAnnouncements(store kvstore.KVStore, sub *kvstore.Subscription, token *kvstore.OAuth2Token) bool {
	if len(sub.ScheduledAnnouncements) == 0 {
		return false
	}

	now := time.Now()
	changed := false
	remaining := sub.ScheduledAnnouncements[:0]
	for _, ann := range sub.ScheduledAnnouncements {
		if ann.DueAt.After(now) {
			remaining = append(remaining, ann)
			continue
		}

		record, err := p.getConferenceRecord(token, ann.ConferenceName)
		if err != nil {
			p.API.LogWarn("Failed to refresh deferred conference record; will retry next poll", "conference", ann.ConferenceName, "error", err.Error())
			remaining = append(remaining, ann)
			continue
		}

		changed = true
		if record.EndTime != nil && record.EndTime.Before(ann.DueAt) {
			p.API.LogInfo("Dropping deferred conference announcement: conference ended before its scheduled start", "conference", ann.ConferenceName, "space_id", sub.SpaceID)
			if err := store.StoreConferencePostState(ann.ConferenceName, &kvstore.ConferencePostState{ChannelID: sub.ChannelID, Suppressed: true}); err != nil {
				p.API.LogWarn("Failed to persist suppressed state for dropped announcement", "conference", ann.ConferenceName, "error", err.Error())
			}
			continue
		}

		instance := &calendarInstance{
			InstanceID: ann.EventInstanceID,
			Summary:    ann.EventSummary,
			Start:      ann.DueAt,
			End:        ann.EventEnd,
		}
		if err := p.postAndBindToCalendarInstance(store, sub, record, instance); err != nil {
			p.API.LogWarn("Failed to post deferred conference announcement; will retry next poll", "conference", ann.ConferenceName, "error", err.Error())
			remaining = append(remaining, ann)
			continue
		}
		// This record's own fetch loop already ran and passed on it while it was deferred, so
		// it needs to be added to ActiveConferenceIDs here for artifact polling to pick it up.
		if !slices.Contains(sub.ActiveConferenceIDs, ann.ConferenceName) {
			sub.ActiveConferenceIDs = append(sub.ActiveConferenceIDs, ann.ConferenceName)
		}
	}
	sub.ScheduledAnnouncements = remaining
	return changed
}

// pruneExpiredEventBindings drops EventPostBindings past their ExpiresAt so a long-lived
// subscription's record doesn't grow without bound.
func pruneExpiredEventBindings(sub *kvstore.Subscription) bool {
	if len(sub.EventPostBindings) == 0 {
		return false
	}
	now := time.Now()
	kept := sub.EventPostBindings[:0]
	for _, binding := range sub.EventPostBindings {
		if binding.ExpiresAt.IsZero() || binding.ExpiresAt.After(now) {
			kept = append(kept, binding)
		}
	}
	changed := len(kept) != len(sub.EventPostBindings)
	sub.EventPostBindings = kept
	return changed
}

func findEventPostBindingIndex(bindings []kvstore.EventPostBinding, instanceID string) int {
	for i := range bindings {
		if bindings[i].EventInstanceID == instanceID {
			return i
		}
	}
	return -1
}

func findEventPostBindingByPostID(bindings []kvstore.EventPostBinding, postID string) int {
	for i := range bindings {
		if bindings[i].MeetingPostID == postID {
			return i
		}
	}
	return -1
}

// maybeMarkConferenceEnded marks state's post as ended when its conference record's end time has
// passed. For a conference record bound to a calendar event instance, the decision is made at the
// instance level: every conference record sharing the binding must be known to have ended, and
// conferenceRejoinGrace must have elapsed since the latest of those ends, before the post flips to
// ended — so a brief drop-and-rejoin within the same scheduled meeting doesn't flap the post.
// Returns true if state was mutated (caller is responsible for persisting it).
func (p *Plugin) maybeMarkConferenceEnded(sub *kvstore.Subscription, state *kvstore.ConferencePostState, confName string, resolvedEnd map[string]*time.Time) bool {
	if state.MeetingEndedPosted {
		return false
	}
	endTime := resolvedEnd[confName]
	if endTime == nil || endTime.IsZero() || endTime.After(time.Now()) {
		return false
	}

	if idx := findEventPostBindingByPostID(sub.EventPostBindings, state.MeetingPostID); idx >= 0 {
		binding := &sub.EventPostBindings[idx]
		if binding.EndedPosted {
			state.MeetingEndedPosted = true
			return true
		}
		latestEnd, allEnded := aggregateBindingEnd(binding.ConferenceNames, resolvedEnd)
		if !allEnded || time.Now().Before(latestEnd.Add(conferenceRejoinGrace)) {
			return false
		}
		if err := p.markMeetingEnded(state.MeetingPostID, &latestEnd); err != nil {
			p.API.LogWarn("Failed to mark calendar-bound meeting as ended", "conference", confName, "post_id", state.MeetingPostID, "error", err.Error())
			return false
		}
		binding.EndedPosted = true
		state.MeetingEndedPosted = true
		return true
	}

	if err := p.markMeetingEnded(state.MeetingPostID, endTime); err != nil {
		p.API.LogWarn("Failed to mark meeting as ended", "conference", confName, "post_id", state.MeetingPostID, "error", err.Error())
		return false
	}
	state.MeetingEndedPosted = true
	return true
}

// aggregateBindingEnd returns the latest known end time across names and whether all of them
// have a known (non-zero) end time yet.
func aggregateBindingEnd(names []string, resolvedEnd map[string]*time.Time) (time.Time, bool) {
	var latest time.Time
	for _, name := range names {
		end := resolvedEnd[name]
		if end == nil || end.IsZero() {
			return time.Time{}, false
		}
		if end.After(latest) {
			latest = *end
		}
	}
	return latest, true
}

// notifyCalendarReconnectNeeded DMs the subscription creator once when the Calendar API reports
// insufficient scopes, so the fallback to the cooldown heuristic isn't silent. Suppressed for a
// day afterward (see kvstore.calendarReconnectNoticeTTL) to avoid spamming every poll cycle.
func (p *Plugin) notifyCalendarReconnectNeeded(store kvstore.KVStore, userID string) {
	sent, err := store.HasCalendarReconnectNoticeSent(userID)
	if err != nil {
		p.API.LogWarn("Failed to check calendar reconnect notice state", "user_id", userID, "error", err.Error())
		return
	}
	if sent {
		return
	}

	if p.botID == "" {
		return
	}
	channel, appErr := p.API.GetDirectChannel(p.botID, userID)
	if appErr != nil {
		p.API.LogWarn("Failed to open DM channel for calendar reconnect notice", "user_id", userID, "error", appErr.Error())
		return
	}
	if channel == nil {
		return
	}

	post := &model.Post{
		UserId:    p.botID,
		ChannelId: channel.Id,
		Message: "Google Meet subscriptions are configured to sync conference-started posts with your calendar schedule, but your connected Google account is missing the Calendar scope. " +
			"Run `/meet connect` to reconnect and grant access — until then, conferences will keep using the cooldown fallback instead of the scheduled time.",
	}
	if _, appErr := p.API.CreatePost(post); appErr != nil {
		p.API.LogWarn("Failed to send calendar reconnect DM", "user_id", userID, "error", appErr.Error())
		return
	}

	if err := store.StoreCalendarReconnectNoticeSent(userID); err != nil {
		p.API.LogWarn("Failed to persist calendar reconnect notice state", "user_id", userID, "error", err.Error())
	}
}

// latestConferenceEndBefore returns the most recent conference end time at or before cutoff,
// considering both the persisted fallback (the subscription's last known conference end) and
// any other fetched records (excluding selfName) whose end time is already known. It anchors
// the conference-start cooldown guard.
func latestConferenceEndBefore(endTimes map[string]*time.Time, selfName string, cutoff, fallback time.Time) time.Time {
	// Only trust the persisted fallback when it precedes the cutoff. A fallback after
	// cutoff means a previous cycle recorded this record's own end (e.g. a post-state
	// persistence failure), which would otherwise produce a negative gap and wrongly
	// suppress the genuine conference on retry.
	var anchor time.Time
	if !fallback.After(cutoff) {
		anchor = fallback
	}
	for name, end := range endTimes {
		if name == selfName || end == nil || end.IsZero() || end.After(cutoff) {
			continue
		}
		if end.After(anchor) {
			anchor = *end
		}
	}
	return anchor
}

// pollConferenceArtifacts checks a single conference record for new recordings/transcripts/smart notes.
// Meeting-ended detection happens in the caller (see maybeMarkConferenceEnded and pollAdHocMeetings),
// since deciding it requires context — calendar-instance aggregation for subscriptions, none for
// ad-hoc — that this function doesn't have.
// Returns true only when the conference's KV state entry is missing (TTL expired), signalling
// that the caller should prune it from ActiveConferenceIDs.
func (p *Plugin) pollConferenceArtifacts(store kvstore.KVStore, token *kvstore.OAuth2Token, confName string) bool {
	state, err := store.GetConferencePostState(confName)
	if err != nil {
		p.API.LogWarn("Failed to get conference post state during artifact poll; will retry", "conference", confName, "error", err.Error())
		return false
	}
	if state == nil {
		return true
	}

	threadRootID := state.ArtifactThreadRoot()

	// Persist state right after each successful post so a single end-of-call
	// KV failure can only re-post one artifact next cycle, not the whole batch.
	// At-least-once: a transient KV failure produces a duplicate Drive/Docs link
	// (visible, recoverable) rather than a silently-dropped artifact.
	persistState := func() {
		if persistErr := store.StoreConferencePostState(confName, state); persistErr != nil {
			p.API.LogWarn("Failed to persist conference post state; artifact may be reposted on retry", "conference", confName, "error", persistErr.Error())
		}
	}

	recordings, err := p.listRecordings(token, confName)
	if err != nil {
		p.API.LogWarn("Failed to list recordings", "conference", confName, "error", err.Error())
	}
	for i := range recordings {
		rec := &recordings[i]
		if rec.State != meetStateFileGenerated {
			continue
		}
		if slices.Contains(state.PostedRecordingIDs, rec.Name) {
			continue
		}
		if err = p.postRecording(state.ChannelID, threadRootID, rec); err != nil {
			p.API.LogWarn("Failed to post recording", "recording", rec.Name, "error", err.Error())
			continue
		}
		if rec.DriveDestination != nil && rec.DriveDestination.ExportURI != "" {
			if linkErr := p.appendMeetingArtifactLink(state.MeetingPostID, artifactLabelRecording, rec.DriveDestination.ExportURI); linkErr != nil {
				p.API.LogWarn("Failed to append recording link to meeting post", "post_id", state.MeetingPostID, "error", linkErr.Error())
			}
		}
		p.API.LogInfo("Posted recording to thread", "recording", rec.Name, "conference", confName, "thread_root_id", threadRootID)
		state.PostedRecordingIDs = append(state.PostedRecordingIDs, rec.Name)
		persistState()
	}

	transcripts, err := p.listTranscripts(token, confName)
	if err != nil {
		p.API.LogWarn("Failed to list transcripts", "conference", confName, "error", err.Error())
	}
	for i := range transcripts {
		tr := &transcripts[i]
		if tr.State != meetStateFileGenerated {
			continue
		}
		if slices.Contains(state.PostedTranscriptIDs, tr.Name) {
			continue
		}
		if err = p.postTranscript(token, state.ChannelID, threadRootID, tr); err != nil {
			p.API.LogWarn("Failed to post transcript", "transcript", tr.Name, "error", err.Error())
			continue
		}
		if tr.DocsDestination != nil && tr.DocsDestination.ExportURI != "" {
			if linkErr := p.appendMeetingArtifactLink(state.MeetingPostID, artifactLabelTranscript, tr.DocsDestination.ExportURI); linkErr != nil {
				p.API.LogWarn("Failed to append transcript link to meeting post", "post_id", state.MeetingPostID, "error", linkErr.Error())
			}
		}
		p.API.LogInfo("Posted transcript to thread", "transcript", tr.Name, "conference", confName, "thread_root_id", threadRootID)
		state.PostedTranscriptIDs = append(state.PostedTranscriptIDs, tr.Name)
		persistState()
	}

	smartNotes, err := p.listSmartNotes(token, confName)
	if err != nil {
		p.API.LogWarn("Failed to list smart notes", "conference", confName, "error", err.Error())
	}
	for i := range smartNotes {
		sn := &smartNotes[i]
		if sn.State != meetStateFileGenerated {
			continue
		}
		if slices.Contains(state.PostedSmartNoteIDs, sn.Name) {
			continue
		}
		if err = p.postSmartNote(state.ChannelID, threadRootID, sn); err != nil {
			p.API.LogWarn("Failed to post smart note", "smart_note", sn.Name, "error", err.Error())
			continue
		}
		if sn.DocsDestination != nil && sn.DocsDestination.ExportURI != "" {
			if linkErr := p.appendMeetingArtifactLink(state.MeetingPostID, artifactLabelSmartNote, sn.DocsDestination.ExportURI); linkErr != nil {
				p.API.LogWarn("Failed to append smart note link to meeting post", "post_id", state.MeetingPostID, "error", linkErr.Error())
			}
		}
		p.API.LogInfo("Posted smart note to thread", "smart_note", sn.Name, "conference", confName, "thread_root_id", threadRootID)
		state.PostedSmartNoteIDs = append(state.PostedSmartNoteIDs, sn.Name)
		persistState()
	}

	return false
}

// pollAdHocMeetings checks all ad-hoc meetings (started via /meet start) for new artifacts.
// Unlike subscriptions, ad-hoc entries are pinned to a specific post that already exists as
// the root, so there is no need to create a conference-started post — we reuse the one
// created by StartMeeting. There is also no calendar binding to aggregate over, so meeting-ended
// detection stays per-record, same as before calendar sync was introduced.
func (p *Plugin) pollAdHocMeetings(store kvstore.KVStore) {
	// Defense in depth: bail early if the admin disabled the feature mid-cycle.
	if !p.getConfiguration().EnableConferenceArtifactPosts {
		return
	}

	spaceIDs, err := store.ListAdHocSpaceIDs()
	if err != nil {
		p.API.LogError("Failed to list ad-hoc space IDs during poll", "error", err.Error())
		return
	}

	for _, spaceID := range spaceIDs {
		entry, err := store.GetAdHocMeetingPost(spaceID)
		if err != nil {
			p.API.LogWarn("Failed to get ad-hoc meeting post", "space_id", spaceID, "error", err.Error())
			continue
		}
		if entry == nil {
			if err = store.RemoveFromAdHocIndex(spaceID); err != nil {
				p.API.LogWarn("Failed to remove expired ad-hoc entry from index", "space_id", spaceID, "error", err.Error())
			}
			continue
		}

		token, err := p.getValidToken(entry.UserID)
		if err != nil {
			p.API.LogWarn("Skipping ad-hoc poll: token lookup failed", "space_id", spaceID, "user_id", entry.UserID, "error", err.Error())
			continue
		}
		if token == nil {
			p.API.LogDebug("Skipping ad-hoc poll: user is not connected to Google", "space_id", spaceID, "user_id", entry.UserID)
			continue
		}

		records, err := p.listConferenceRecords(token, spaceID, entry.CreatedAt)
		if err != nil {
			p.API.LogWarn("Failed to list conference records for ad-hoc space", "space_id", spaceID, "error", err.Error())
			continue
		}

		for i := range records {
			record := &records[i]
			state, err := store.GetConferencePostState(record.Name)
			if err != nil {
				p.API.LogWarn("Failed to get conference post state for ad-hoc meeting", "conference", record.Name, "error", err.Error())
				continue
			}

			if state == nil {
				// Pin the conference to the existing /meet start post instead of creating a new one.
				state = &kvstore.ConferencePostState{
					MeetingPostID: entry.MeetingPostID,
					ThreadRootID:  entry.ThreadRootID,
					ChannelID:     entry.ChannelID,
				}
				if err := store.StoreConferencePostState(record.Name, state); err != nil {
					p.API.LogWarn("Failed to store conference post state for ad-hoc meeting", "conference", record.Name, "error", err.Error())
					continue
				}
			}

			if record.EndTime != nil && !record.EndTime.IsZero() && record.EndTime.Before(time.Now()) && !state.MeetingEndedPosted {
				if endErr := p.markMeetingEnded(state.MeetingPostID, record.EndTime); endErr != nil {
					p.API.LogWarn("Failed to mark ad-hoc meeting as ended", "conference", record.Name, "post_id", state.MeetingPostID, "error", endErr.Error())
				} else {
					state.MeetingEndedPosted = true
					if err := store.StoreConferencePostState(record.Name, state); err != nil {
						p.API.LogWarn("Failed to persist ad-hoc meeting-ended state", "conference", record.Name, "error", err.Error())
					}
				}
			}

			p.pollConferenceArtifacts(store, token, record.Name)
		}
	}
}
