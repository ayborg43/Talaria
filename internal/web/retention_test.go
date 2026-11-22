package web

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"talaria/internal/config"
	"talaria/internal/provider"
)

func shelf(t *testing.T) (*Convos, map[string]string) {
	t.Helper()
	cv, err := OpenConvos(filepath.Join(t.TempDir(), "chats.json"))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	mk := func(name string, age int) string {
		id := cv.New()
		cv.Save(id, nil, []provider.Message{{Role: provider.RoleUser, Content: name}})
		cv.mu.Lock()
		cv.items[id].UpdatedAt = time.Now().AddDate(0, 0, -age)
		cv.mu.Unlock()
		ids[name] = id
		return id
	}
	mk("main", 400)
	mk("stale", 100)
	mk("fresh", 2)
	mk("channel", 300)
	mk("with-agent", 300)
	mk("active", 500)
	cv.mu.Lock()
	cv.main = ids["main"]
	cv.items[ids["channel"]].Purpose = "the garden"
	cv.items[ids["with-agent"]].Agent = "a111"
	cv.active = ids["active"]
	cv.mu.Unlock()
	return cv, ids
}

func (c *Convos) has(id string) (archived, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cv, ok := c.items[id]
	if !ok {
		return false, false
	}
	return cv.Archived, true
}

func TestSweepArchivesStaleChatsAndSparesWhatIsKeptOnPurpose(t *testing.T) {
	cv, ids := shelf(t)
	now := time.Now()
	dry := cv.Sweep(now.AddDate(0, 0, -90), now.AddDate(0, 0, -30), true)
	if dry.Archived != 1 || dry.Deleted != 0 {
		t.Fatalf("a dry run counts: %+v", dry)
	}
	if a, _ := cv.has(ids["stale"]); a {
		t.Fatal("a dry run changed something")
	}
	res := cv.Sweep(now.AddDate(0, 0, -90), now.AddDate(0, 0, -30), false)
	if res.Archived != 1 || res.Deleted != 0 {
		t.Fatalf("%+v", res)
	}
	for name, wantArchived := range map[string]bool{"stale": true, "fresh": false, "main": false, "channel": false, "with-agent": false, "active": false} {
		if a, ok := cv.has(ids[name]); !ok || a != wantArchived {
			t.Errorf("%s: archived=%v present=%v, want archived=%v", name, a, ok, wantArchived)
		}
	}
	// A month later the one the sweep archived goes for good.
	later := now.AddDate(0, 0, 31)
	res = cv.Sweep(later.AddDate(0, 0, -90), later.AddDate(0, 0, -30), false)
	if res.Deleted != 1 {
		t.Fatalf("%+v", res)
	}
	if _, ok := cv.has(ids["stale"]); ok {
		t.Fatal("it should be deleted now")
	}
	if _, ok := cv.has(ids["main"]); !ok {
		t.Fatal("the main conversation was deleted")
	}
}

func TestAConversationYouArchivedYourselfIsNeverDeleted(t *testing.T) {
	cv, ids := shelf(t)
	if err := cv.SetArchived(ids["stale"], true); err != nil {
		t.Fatal(err)
	}
	later := time.Now().AddDate(0, 0, 400)
	res := cv.Sweep(time.Time{}, later.AddDate(0, 0, -30), false)
	if res.Deleted != 0 {
		t.Fatalf("%+v", res)
	}
	if _, ok := cv.has(ids["stale"]); !ok {
		t.Fatal("a chat the person archived was deleted")
	}
	// Restoring one the sweep archived takes the mark off it.
	cv2, ids2 := shelf(t)
	cv2.Sweep(time.Now().AddDate(0, 0, -90), time.Time{}, false)
	cv2.SetArchived(ids2["stale"], false)
	cv2.Sweep(time.Time{}, time.Now().AddDate(0, 0, 400), false)
	if _, ok := cv2.has(ids2["stale"]); !ok {
		t.Fatal("a restored chat was deleted")
	}
	// With the setting off nothing happens at all.
	cv3, ids3 := shelf(t)
	if res := cv3.Sweep(time.Time{}, time.Time{}, false); res != (SweepResult{}) {
		t.Fatalf("%+v", res)
	}
	if a, _ := cv3.has(ids3["stale"]); a {
		t.Fatal("archived with the setting off")
	}
}

func TestRetentionEndpointValidatesSavesAndSweeps(t *testing.T) {
	r := newRig(t)
	r.login()
	if _, body := r.do("GET", "/api/retention", nil); !contains(string(body), `"available":false`) {
		t.Fatalf("%s", body)
	}
	cv, ids := shelf(t)
	dir := t.TempDir()
	r.srv.Convos, r.srv.Cfg, r.srv.ConfigPath = cv, &config.Config{}, filepath.Join(dir, "config.json")
	pruned := 0
	r.srv.Sources.PruneHistory = func(before time.Time) (int, error) { pruned++; return 3, nil }

	for _, bad := range []map[string]any{{"keep_days": 3}, {"keep_days": 99999}, {"history_keep_days": 10}} {
		if resp, _ := r.do("POST", "/api/retention", bad); resp.StatusCode != 400 {
			t.Errorf("%v accepted: %d", bad, resp.StatusCode)
		}
	}
	// Off by default: the preview says nothing would happen.
	_, body := r.do("GET", "/api/retention", nil)
	var v retentionView
	json.Unmarshal(body, &v)
	if !v.Available || v.KeepDays != 0 || v.WouldArchive != 0 || !v.CanPruneHistory {
		t.Fatalf("%+v", v)
	}
	resp, body := r.do("POST", "/api/retention", map[string]any{"keep_days": 90, "history_keep_days": 180})
	json.Unmarshal(body, &v)
	if resp.StatusCode != 200 || v.KeepDays != 90 || v.HistoryKeepDays != 180 || v.WouldArchive != 1 {
		t.Fatalf("%d %+v", resp.StatusCode, v)
	}
	if saved, err := config.Load(r.srv.ConfigPath); err != nil || saved.Conversations.KeepDays != 90 || saved.Conversations.HistoryKeepDays != 180 {
		t.Fatalf("not saved: %v %+v", err, saved)
	}
	if a, _ := cv.has(ids["stale"]); a {
		t.Fatal("saving the setting must not sweep by itself")
	}
	_, body = r.do("POST", "/api/retention", map[string]any{"sweep": true})
	json.Unmarshal(body, &v)
	if a, _ := cv.has(ids["stale"]); !a || pruned != 1 || v.LastArchived != 1 || v.LastPruned != 3 {
		t.Fatalf("archived=%v pruned=%d %+v", a, pruned, v)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
