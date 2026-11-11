package telegram

import "testing"

func containerTestClient() *Client {
	d := &UpdateDispatcher{stopped: true}
	d.globalSeqBox = newCounterBox("seq", nil, nil, nil)
	d.globalPtsBox = newCounterBox("pts", nil, nil, nil)
	d.globalQtsBox = newCounterBox("qts", nil, nil, nil)
	d.SetSeq(10)
	d.SetPts(100)
	d.SetQts(200)
	return &Client{dispatcher: d, Cache: NewCache("", &CacheConfig{Memory: true})}
}

func TestContainerMessageBoxesIndependentOfSeq(t *testing.T) {
	for _, combined := range []bool{false, true} {
		for _, seq := range []int32{9, 12} { // stale and gapped envelopes
			c := containerTestClient()
			updates := []Update{
				&UpdateReadHistoryInbox{Pts: 101, PtsCount: 1},
				&UpdateBotStopped{Qts: 201},
			}
			var envelope any = &UpdatesObj{Updates: updates, Seq: seq, Date: 1234}
			if combined {
				envelope = &UpdatesCombined{Updates: updates, Seq: seq, SeqStart: seq, Date: 1234}
			}
			HandleIncomingUpdates(envelope, c)
			if c.dispatcher.GetPts() != 101 || c.dispatcher.GetQts() != 201 {
				t.Fatalf("combined=%v seq=%d suppressed message boxes: pts=%d qts=%d", combined, seq, c.dispatcher.GetPts(), c.dispatcher.GetQts())
			}
			if c.dispatcher.GetSeq() != 10 || c.dispatcher.GetDate() != 0 {
				t.Fatal("unordered envelope advanced seq/date")
			}
			// Duplicate message boxes stay deduplicated when the seq gap closes.
			HandleIncomingUpdates(&UpdatesObj{Seq: 11, Date: 1200}, c)
			if c.dispatcher.GetPts() != 101 || c.dispatcher.GetQts() != 201 {
				t.Fatal("flushing seq changed independent counters")
			}
			wantSeq, wantDate := int32(11), int32(1200)
			if seq == 12 {
				wantSeq, wantDate = 12, 1234
			}
			if c.dispatcher.GetSeq() != wantSeq || c.dispatcher.GetDate() != wantDate {
				t.Fatalf("seq gap did not retain envelope ordering: seq=%d date=%d", c.dispatcher.GetSeq(), c.dispatcher.GetDate())
			}
		}
	}
}

func TestContainerQtsGapDoesNotBlockPts(t *testing.T) {
	c := containerTestClient()
	HandleIncomingUpdates(&UpdatesObj{Seq: 11, Updates: []Update{
		&UpdateBotStopped{Qts: 202},
		&UpdateReadHistoryInbox{Pts: 101, PtsCount: 1},
	}}, c)
	if c.dispatcher.GetPts() != 101 || c.dispatcher.GetQts() != 200 || c.dispatcher.GetSeq() != 11 {
		t.Fatal("guest/bot QTS gap blocked another sequence")
	}
	HandleIncomingUpdates(&UpdateShort{Update: &UpdateBotStopped{Qts: 201}}, c)
	if c.dispatcher.GetQts() != 202 {
		t.Fatal("QTS gap failed to flush")
	}
}
