// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"slices"
	"time"

	"github.com/mattermost/mattermost/server/public/pluginapi/cluster"

	"github.com/mattermost/mattermost-plugin-google-meet/server/store/kvstore"
)

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

	records, err := p.listConferenceRecords(token, sub.SpaceID, sub.LastSeenConferenceStart)
	if err != nil {
		p.API.LogWarn("Failed to list conference records", "space_id", sub.SpaceID, "error", err.Error())
		records = nil
	}

	// Buffer the high-water mark advance until the entire batch succeeds.
	// Advancing eagerly per-record would skip past a failed record whenever a
	// later (newer) record in the same batch succeeded — the failed one is
	// older than the new LastSeen and would never be re-fetched. Successful
	// records still get added to ActiveConferenceIDs so their artifacts are
	// polled even on partial-batch failure.
	hadFailure := false
	candidateLastSeen := sub.LastSeenConferenceStart
	subChanged := false

	for i := range records {
		record := &records[i]
		state, err := store.GetConferencePostState(record.Name)
		if err != nil {
			p.API.LogWarn("Failed to get conference post state", "conference", record.Name, "error", err.Error())
			hadFailure = true
			continue
		}

		if state == nil {
			postID, err := p.postConferenceStarted(sub, record)
			if err != nil {
				p.API.LogWarn("Failed to post conference started", "conference", record.Name, "error", err.Error())
				hadFailure = true
				continue
			}
			p.API.LogInfo("Posted new Google Meet conference notification", "conference", record.Name, "space_id", sub.SpaceID, "channel_id", sub.ChannelID, "root_post_id", postID)
			state = &kvstore.ConferencePostState{
				RootPostID: postID,
				ChannelID:  sub.ChannelID,
			}
			if err := store.StoreConferencePostState(record.Name, state); err != nil {
				p.API.LogWarn("Failed to store conference post state; will retry on next poll", "conference", record.Name, "error", err.Error())
				hadFailure = true
				continue
			}
		}

		if record.StartTime != nil && record.StartTime.After(candidateLastSeen) {
			candidateLastSeen = *record.StartTime
		}
		if !slices.Contains(sub.ActiveConferenceIDs, record.Name) {
			sub.ActiveConferenceIDs = append(sub.ActiveConferenceIDs, record.Name)
			subChanged = true
		}
	}

	if !hadFailure && candidateLastSeen.After(sub.LastSeenConferenceStart) {
		sub.LastSeenConferenceStart = candidateLastSeen
		subChanged = true
	}
	if subChanged {
		if err := store.StoreSubscription(sub); err != nil {
			p.API.LogWarn("Failed to update subscription state", "space_id", sub.SpaceID, "error", err.Error())
		}
	}

	// Prune conferences whose post-state has expired (TTL) so ActiveConferenceIDs
	// doesn't grow unbounded over the life of the subscription.
	stillActive := sub.ActiveConferenceIDs[:0]
	for _, confName := range sub.ActiveConferenceIDs {
		if done := p.pollConferenceArtifacts(store, token, confName); !done {
			stillActive = append(stillActive, confName)
		}
	}
	if len(stillActive) != len(sub.ActiveConferenceIDs) {
		sub.ActiveConferenceIDs = stillActive
		if err := store.StoreSubscription(sub); err != nil {
			p.API.LogWarn("Failed to persist pruned subscription state", "space_id", sub.SpaceID, "error", err.Error())
		}
	}
}

// pollConferenceArtifacts checks a single conference record for new recordings/transcripts/smart notes.
// Returns true only when the conference's KV state entry is missing (TTL expired), signalling
// that the caller should prune it from ActiveConferenceIDs. Transient read errors return false
// so a flaky KV doesn't silently stop monitoring an in-progress conference.
func (p *Plugin) pollConferenceArtifacts(store kvstore.KVStore, token *kvstore.OAuth2Token, confName string) bool {
	state, err := store.GetConferencePostState(confName)
	if err != nil {
		p.API.LogWarn("Failed to get conference post state during artifact poll; will retry", "conference", confName, "error", err.Error())
		return false
	}
	if state == nil {
		return true
	}

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
		if err = p.postRecording(state.ChannelID, state.RootPostID, rec); err != nil {
			p.API.LogWarn("Failed to post recording", "recording", rec.Name, "error", err.Error())
			continue
		}
		p.API.LogInfo("Posted recording to thread", "recording", rec.Name, "conference", confName, "root_post_id", state.RootPostID)
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
		if err = p.postTranscript(token, state.ChannelID, state.RootPostID, tr); err != nil {
			p.API.LogWarn("Failed to post transcript", "transcript", tr.Name, "error", err.Error())
			continue
		}
		p.API.LogInfo("Posted transcript to thread", "transcript", tr.Name, "conference", confName, "root_post_id", state.RootPostID)
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
		if err = p.postSmartNote(state.ChannelID, state.RootPostID, sn); err != nil {
			p.API.LogWarn("Failed to post smart note", "smart_note", sn.Name, "error", err.Error())
			continue
		}
		p.API.LogInfo("Posted smart note to thread", "smart_note", sn.Name, "conference", confName, "root_post_id", state.RootPostID)
		state.PostedSmartNoteIDs = append(state.PostedSmartNoteIDs, sn.Name)
		persistState()
	}

	return false
}

// pollAdHocMeetings checks all ad-hoc meetings (started via /meet start) for new artifacts.
// Unlike subscriptions, ad-hoc entries are pinned to a specific post that already exists as
// the root, so there is no need to create a conference-started post — we reuse the one
// created by StartMeeting.
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
					RootPostID: entry.RootPostID,
					ChannelID:  entry.ChannelID,
				}
				if err := store.StoreConferencePostState(record.Name, state); err != nil {
					p.API.LogWarn("Failed to store conference post state for ad-hoc meeting", "conference", record.Name, "error", err.Error())
					continue
				}
			}

			p.pollConferenceArtifacts(store, token, record.Name)
		}
	}
}
