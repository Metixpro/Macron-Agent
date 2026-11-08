package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTest(t *testing.T, opts Options) *Store {
	t.Helper()
	if opts.Location == nil {
		opts.Location, _ = time.LoadLocation("Asia/Tehran")
	}
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

var bg = context.Background()

func TestMigrationsAreIdempotent(t *testing.T) {
	s := openTest(t, Options{})
	if err := s.migrate(bg); err != nil {
		t.Fatal(err)
	}
	var v int
	s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	if v != len(migrations) {
		t.Fatalf("version %d, want %d", v, len(migrations))
	}
}

func TestUsersAndProfile(t *testing.T) {
	s := openTest(t, Options{})
	u, isNew, err := s.TouchUser(bg, UserInfo{ID: 42, Username: "hajmeti", FirstName: "حاج", LastName: "متی 👑", LanguageCode: "fa", IsPremium: true})
	if err != nil || !isNew || u.FirstName != "حاج" || u.LastName != "متی 👑" || !u.IsPremium {
		t.Fatalf("%+v new=%v err=%v", u, isNew, err)
	}
	u, isNew, _ = s.TouchUser(bg, UserInfo{ID: 42, Username: "hajmeti2", FirstName: "Meti"})
	if isNew || u.Username != "hajmeti2" || u.FirstName != "Meti" {
		t.Fatalf("update: %+v new=%v", u, isNew)
	}
	if u.DisplayName() != "Meti" {
		t.Errorf("display name %q", u.DisplayName())
	}
	if err := s.SetProfileDetails(bg, 42, "برنامه‌نویس Go 🚀", "1383-05-01"); err != nil {
		t.Fatal(err)
	}
	u, _ = s.User(bg, 42)
	if u.Bio != "برنامه‌نویس Go 🚀" || u.Birthdate != "1383-05-01" || u.ProfileCheckedAt == nil {
		t.Errorf("profile: %+v", u)
	}
	if _, err := s.User(bg, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing user: %v", err)
	}

	s.TouchUser(bg, UserInfo{ID: 7, FirstName: "Ali", Username: "ali_x"})
	s.TouchUser(bg, UserInfo{ID: 8, FirstName: "100%_real"})
	for q, want := range map[string]int64{"42": 42, "@ali_x": 7, "ali": 7, "100%": 8} {
		got, err := s.FindUsers(bg, q, 10)
		if err != nil || len(got) != 1 || got[0].ID != want {
			t.Errorf("FindUsers(%q) = %+v, %v", q, got, err)
		}
	}
	if got, _ := s.FindUsers(bg, "%", 10); len(got) != 1 || got[0].ID != 8 {
		t.Errorf("LIKE wildcards must be escaped: %+v", got)
	}

	s.SetBlockedBot(bg, 8, true)

	if u, _, _ := s.TouchUser(bg, UserInfo{ID: 8, FirstName: "x"}); u.BlockedBot {
		t.Error("blocked flag must clear when the user writes again")
	}
	users, total, _ := s.RecentUsers(bg, 0, 2)
	if total != 3 || len(users) != 2 {
		t.Errorf("recent: %d/%d", len(users), total)
	}
}

func TestDailyTokens(t *testing.T) {
	now := time.Date(2026, 10, 18, 20, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s := openTest(t, Options{Now: clock})
	s.TouchUser(bg, UserInfo{ID: 1, FirstName: "a"})

	for range 3 {
		_ = s.CountRequest(bg, 1)
	}
	_ = s.Refund(bg, 1)
	if n, _ := s.UsedToday(bg, 1); n != 2 {
		t.Errorf("requests: %d", n)
	}
	_ = s.AddTokens(bg, 1, 600)
	_ = s.AddTokens(bg, 1, 400)
	if n, _ := s.TokensToday(bg, 1); n != 1000 {
		t.Errorf("today's tokens: %d", n)
	}

	now = now.Add(31 * time.Minute)
	if n, _ := s.TokensToday(bg, 1); n != 0 {
		t.Errorf("a new local day starts over: %d", n)
	}
	u, _ := s.User(bg, 1)
	if u.MessagesTotal != 2 {
		t.Errorf("messages_total=%d", u.MessagesTotal)
	}
}

func TestMemory(t *testing.T) {
	s := openTest(t, Options{})
	big := strings.Repeat("سلام دنیا 🌍 ", 2000)
	if err := s.AppendContext(bg, 1, ContextMessage{Role: "user", Content: "q1", Tokens: 3}, ContextMessage{Role: "assistant", Content: big, Tokens: 9000}); err != nil {
		t.Fatal(err)
	}
	s.AppendContext(bg, 1, ContextMessage{Role: "user", Content: "q2", Tokens: 4})
	s.AppendContext(bg, 2, ContextMessage{Role: "user", Content: "other", Tokens: 1})
	m, err := s.LoadMemory(bg, 1)
	if err != nil || len(m.Messages) != 3 || m.Messages[1].Content != big || m.Tokens() != 9007 {
		t.Fatalf("load: %d msgs, %d tokens, %v", len(m.Messages), m.Tokens(), err)
	}
	if err := s.Compact(bg, 1, m.Messages[1].ID, "خلاصه", 50); err != nil {
		t.Fatal(err)
	}
	m, _ = s.LoadMemory(bg, 1)
	if len(m.Messages) != 1 || m.Messages[0].Content != "q2" || m.Summary != "خلاصه" || m.Compressions != 1 || m.Tokens() != 54 {
		t.Fatalf("after compact: %+v", m)
	}
	s.Compact(bg, 1, 0, "خلاصه ۲", 60)
	if m, _ = s.LoadMemory(bg, 1); m.Compressions != 2 || m.Summary != "خلاصه ۲" {
		t.Errorf("second compaction: %+v", m)
	}
	s.ClearMemory(bg, 1)
	if m, _ = s.LoadMemory(bg, 1); len(m.Messages) != 0 || m.Summary != "" {
		t.Errorf("clear: %+v", m)
	}
	if m, _ = s.LoadMemory(bg, 2); len(m.Messages) != 1 {
		t.Error("other users are untouched")
	}
}

func TestProvidersAndEncryption(t *testing.T) {
	s := openTest(t, Options{SecretKey: "unit-test-secret"})
	id, err := s.AddProvider(bg, Provider{Name: "main", BaseURL: "https://a/v1", APIKey: "sk-secret", Model: "m1", Priority: 10, Enabled: true, Vision: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddProvider(bg, Provider{Name: "main", BaseURL: "x", Model: "y"}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate name: %v", err)
	}
	var raw string
	s.db.QueryRow(`SELECT api_key FROM providers WHERE id = ?`, id).Scan(&raw)
	if strings.Contains(raw, "sk-secret") || !strings.HasPrefix(raw, encPrefix) {
		t.Fatalf("key stored in clear: %q", raw)
	}
	p, err := s.Provider(bg, id)
	if err != nil || p.APIKey != "sk-secret" {
		t.Fatalf("decrypt: %+v %v", p, err)
	}

	s.AddProvider(bg, Provider{Name: "backup", BaseURL: "https://b/v1", APIKey: "k2", Model: "m2", Priority: 5, Enabled: true})
	list, _ := s.Providers(bg)
	if len(list) != 2 || list[0].Name != "backup" {
		t.Errorf("order by priority: %+v", list)
	}
	s.RecordProviderResult(bg, id, true, 200*time.Millisecond, "")
	s.RecordProviderResult(bg, id, true, 400*time.Millisecond, "")
	s.RecordProviderResult(bg, id, false, time.Second, "HTTP 503")
	p, _ = s.Provider(bg, id)
	if p.Requests != 3 || p.Failures != 1 || p.LastError != "HTTP 503" || p.LastOKAt == nil || p.AvgLatencyMS != 240 {
		t.Errorf("health: %+v", p)
	}
	p.Model, p.Enabled = "m1-pro", false
	if err := s.UpdateProvider(bg, *p); err != nil {
		t.Fatal(err)
	}
	p, _ = s.Provider(bg, id)
	if p.Model != "m1-pro" || p.Enabled || p.APIKey != "sk-secret" {
		t.Errorf("update: %+v", p)
	}
	s.DeleteProvider(bg, id)
	if _, err := s.Provider(bg, id); !errors.Is(err, ErrNotFound) {
		t.Error("delete")
	}

	s2 := &Store{db: s.db, now: time.Now}
	list, _ = s2.Providers(bg)
	if len(list) == 0 {
		t.Fatal("expected the backup provider")
	}
	enc, _ := (&Store{aead: s.aead}).open(s.seal("abc"))
	if enc != "abc" {
		t.Error("round trip")
	}
	if _, err := s2.open(s.seal("abc")); err == nil {
		t.Error("decrypting without a key must fail loudly")
	}
}

func TestSettingsAdminsChannelsEmoji(t *testing.T) {
	s := openTest(t, Options{})
	if s.SettingBool(SettingFailover, false) || s.SettingInt(SettingHistoryTokens, 8000) != 8000 {
		t.Error("defaults")
	}
	s.SetSetting(bg, SettingFailover, "true")
	s.SetSetting(bg, SettingHistoryTokens, "12000")
	s.SetSetting(bg, "unicode_test_setting", "سلام <b>دوست</b> 👋")

	s2 := &Store{db: s.db, now: time.Now, settings: map[string]string{}, admins: map[int64]bool{}}
	s2.reloadCaches(bg)
	if !s2.SettingBool(SettingFailover, false) || s2.SettingInt(SettingHistoryTokens, 0) != 12000 {
		t.Error("persisted settings")
	}
	if v, _ := s2.Setting("unicode_test_setting"); v != "سلام <b>دوست</b> 👋" {
		t.Errorf("welcome %q", v)
	}
	s.DeleteSetting(bg, "unicode_test_setting")
	if _, ok := s.Setting("unicode_test_setting"); ok {
		t.Error("delete setting")
	}

	s.AddAdmin(bg, 10, 1)
	if !s.IsAdmin(10) || s.IsAdmin(11) {
		t.Error("admins")
	}
	ids, _ := s.Admins(bg)
	if len(ids) != 1 {
		t.Error("list admins")
	}
	s.RemoveAdmin(bg, 10)
	if s.IsAdmin(10) {
		t.Error("remove admin")
	}

	pairs := map[string]string{"😀": "111", "🔥": "222", "❤️": "333", "👍🏽": "444", "🏳️‍🌈": "555"}
	for e, id := range pairs {
		if err := s.SetPremiumEmoji(bg, e, id); err != nil {
			t.Fatal(err)
		}
	}
	s.SetPremiumEmoji(bg, "😀", "999")
	got, _ := s.PremiumEmoji(bg)
	if len(got) != 5 || got["😀"] != "999" || got["🔥"] != "222" || got["🏳️‍🌈"] != "555" {
		t.Fatalf("emoji map: %v", got)
	}
	s.DeletePremiumEmoji(bg, "🔥")
	if got, _ = s.PremiumEmoji(bg); len(got) != 4 || got["🔥"] != "" {
		t.Errorf("delete emoji: %v", got)
	}
	s.ClearPremiumEmoji(bg)
	if got, _ = s.PremiumEmoji(bg); len(got) != 0 {
		t.Error("clear emoji")
	}
}

func TestStats(t *testing.T) {
	s := openTest(t, Options{})
	for i := int64(1); i <= 4; i++ {
		s.TouchUser(bg, UserInfo{ID: i, FirstName: "user", IsPremium: i == 1})
	}
	for i := 0; i < 5; i++ {
		s.CountRequest(bg, 1)
	}
	s.CountRequest(bg, 2)
	s.RecordStat(bg, StatTool, "web_search", false, 0, 0)
	s.RecordStat(bg, StatTool, "web_search", true, 0, 0)
	s.RecordStat(bg, StatTool, "generate_chart", false, 0, 0)
	s.RecordStat(bg, StatProvider, "main", false, 1000, 200)
	s.AppendContext(bg, 1, ContextMessage{Role: "user", Content: "x", Tokens: 1})
	s.AddProvider(bg, Provider{Name: "p", BaseURL: "u", Model: "m", Enabled: true})
	s.SetPremiumEmoji(bg, "😀", "1")

	o, err := s.Overview(bg)
	if err != nil {
		t.Fatal(err)
	}
	if o.Users != 4 || o.NewToday != 4 || o.ActiveToday != 4 || o.RequestsToday != 6 || o.Requests7d != 6 || o.Premium != 1 ||
		o.TokensTodayPrompt != 1000 || o.TokensTodayCompletion != 200 || o.MemoryUsers != 1 || o.Providers != 1 || o.PremiumEmoji != 1 || o.RequestsTotal != 6 {
		t.Fatalf("overview: %+v", o)
	}
	series, err := s.DailySeries(bg, 14)
	if err != nil || len(series) != 14 {
		t.Fatalf("series: %v %v", series, err)
	}
	last := series[13]
	if last.Day != s.Today() || last.Requests != 6 || last.Users != 2 || last.NewUsers != 4 {
		t.Errorf("today point: %+v", last)
	}
	top, _ := s.TopUsersToday(bg, 3)
	if len(top) != 2 || top[0].ID != 1 || top[0].Count != 5 {
		t.Errorf("top users: %+v", top)
	}
	tools, _ := s.TopStats(bg, StatTool, 7, 5)
	if len(tools) != 2 || tools[0].Name != "web_search" || tools[0].Count != 2 || tools[0].Failures != 1 {
		t.Errorf("top tools: %+v", tools)
	}
	if size, err := s.DatabaseSize(bg); err != nil || size <= 0 {
		t.Errorf("db size %d %v", size, err)
	}
}

func TestChats(t *testing.T) {
	s := openTest(t, Options{})
	ctx := context.Background()
	if err := s.SaveChatJoin(ctx, BotChat{ChatID: -1001, Type: "supergroup", Title: "Go", Status: "member", Members: 30, AddedBy: 7, AddedByName: "Ali"}); err != nil {
		t.Fatal(err)
	}

	if err := s.SaveChatJoin(ctx, BotChat{ChatID: -1001, Type: "supergroup", Title: "Go!", Status: "administrator"}); err != nil {
		t.Fatal(err)
	}
	c, err := s.Chat(ctx, -1001)
	if err != nil || c.Status != "administrator" || c.Title != "Go!" || c.AddedBy != 7 || c.Members != 30 || c.JoinedAt == nil {
		t.Fatalf("after promotion: %+v %v", c, err)
	}
	_ = s.SaveChatLeave(ctx, -1001, "kicked", "")
	_ = s.TouchChat(ctx, -1002, "group", "Second", "")
	if c, _ := s.Chat(ctx, -1001); c.Active() || c.LeftAt == nil || c.Title != "Go!" {
		t.Errorf("after removal: %+v", c)
	}

	_ = s.SaveChatJoin(ctx, BotChat{ChatID: -1001, Type: "supergroup", Title: "Go", Status: "member", AddedBy: 9, AddedByName: "Sara"})
	if c, _ := s.Chat(ctx, -1001); !c.Active() || c.AddedBy != 9 || c.LeftAt != nil {
		t.Errorf("after re-adding: %+v", c)
	}
	active, left, members, err := s.ChatCounts(ctx)
	if err != nil || active != 2 || left != 0 || members != 30 {
		t.Errorf("counts: %d %d %d %v", active, left, members, err)
	}
	list, _ := s.Chats(ctx, 10)
	if len(list) != 2 {
		t.Errorf("list: %+v", list)
	}
	if _, err := s.Chat(ctx, -5); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing chat: %v", err)
	}
}

func TestPolls(t *testing.T) {
	s := openTest(t, Options{})
	ctx := context.Background()
	if err := s.SavePoll(ctx, Poll{PollID: "5012345678901", UserID: 7, ChatID: -100, ChatType: "supergroup", MessageID: 33,
		Question: "کدوم سبک؟", Options: []string{"مینیمال", "رنگی"}, Multiple: true}); err != nil {
		t.Fatal(err)
	}
	p, err := s.Poll(ctx, "5012345678901")
	if err != nil || p.UserID != 7 || p.ChatType != "supergroup" || p.Options[1] != "رنگی" || !p.Multiple || p.AnsweredAt != nil {
		t.Fatalf("poll: %+v %v", p, err)
	}
	if ok, err := s.MarkPollAnswered(ctx, p.PollID); !ok || err != nil {
		t.Errorf("first answer: %v %v", ok, err)
	}
	if ok, _ := s.MarkPollAnswered(ctx, p.PollID); ok {
		t.Error("a poll is answered once")
	}
	if _, err := s.Poll(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing poll: %v", err)
	}
	if n, err := s.DeleteOldPolls(ctx, -time.Hour); err != nil || n != 1 {
		t.Errorf("cleanup: %d %v", n, err)
	}
}

func TestSchedules(t *testing.T) {
	s := openTest(t, Options{})
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	id, err := s.AddSchedule(ctx, Schedule{UserID: 7, ChatID: 7, ChatType: "private", Kind: ScheduleReminder, Title: "زنگ", Body: "به مهدی زنگ بزن",
		NextRun: now.Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.AddSchedule(ctx, Schedule{UserID: 7, ChatID: -100, ChatType: "group", Kind: ScheduleTask, Body: "قیمت طلا", Rule: "daily@10:00",
		NextRun: now.Add(time.Hour)})
	due, _ := s.DueSchedules(ctx, now, 10)
	if len(due) != 1 || due[0].ID != id || due[0].Body != "به مهدی زنگ بزن" {
		t.Fatalf("due: %+v", due)
	}

	if ok, err := s.ClaimSchedule(ctx, due[0], time.Time{}); !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if ok, _ := s.ClaimSchedule(ctx, due[0], time.Time{}); ok {
		t.Error("a run is claimed once")
	}
	if x, _ := s.Schedule(ctx, id); x.Status != ScheduleDone || x.Runs != 1 || x.LastRun == nil {
		t.Errorf("a one-time schedule is done after its run: %+v", x)
	}
	byUser, byChat, _ := s.CountActiveSchedules(ctx, 7, -100)
	if byUser != 1 || byChat != 1 {
		t.Errorf("counts: %d %d", byUser, byChat)
	}
	list, _ := s.UserSchedules(ctx, 7)
	task := list[0]
	for i := 0; i < 3; i++ {
		paused, _ := s.RecordScheduleResult(ctx, task.ID, "chat not found", 3)
		if paused != (i == 2) {
			t.Errorf("failure %d: paused=%v (only the third one pauses it)", i+1, paused)
		}
	}
	if x, _ := s.Schedule(ctx, task.ID); x.Status != SchedulePaused || x.LastError != "chat not found" {
		t.Errorf("repeated failures pause a schedule: %+v", x)
	}
	if ok, _ := s.CancelSchedule(ctx, task.ID, 8); ok {
		t.Error("only the owner cancels")
	}
	if ok, _ := s.CancelSchedule(ctx, task.ID, 7); !ok {
		t.Error("the owner cancels")
	}
	if r, k, _ := s.ScheduleCounts(ctx); r != 0 || k != 0 {
		t.Errorf("counts after: %d %d", r, k)
	}
}

func TestFactsAndPrefs(t *testing.T) {
	s := openTest(t, Options{})
	ctx := context.Background()
	const uid = 777
	id1, existed, err := s.AddFact(ctx, uid, "  Lives in Mashhad ")
	if err != nil || existed || id1 == 0 {
		t.Fatalf("add: %d %v %v", id1, existed, err)
	}
	if id, existed, _ := s.AddFact(ctx, uid, "lives in mashhad"); !existed || id != id1 {
		t.Error("the same fact is not saved twice")
	}
	id2, _, _ := s.AddFact(ctx, uid, strings.Repeat("ج", 400))
	facts, _ := s.Facts(ctx, uid)
	if len(facts) != 2 || facts[0].Text != "Lives in Mashhad" || len([]rune(facts[1].Text)) != MaxFactChars {
		t.Errorf("facts: %+v", facts)
	}
	if ok, _ := s.UpdateFact(ctx, uid, id1, "Lives in Tehran"); !ok {
		t.Error("update")
	}
	if ok, _ := s.UpdateFact(ctx, 999, id1, "x"); ok {
		t.Error("someone else's fact cannot be changed")
	}
	if ok, _ := s.DeleteFact(ctx, 999, id2); ok {
		t.Error("someone else's fact cannot be deleted")
	}
	if ok, _ := s.DeleteFact(ctx, uid, id2); !ok {
		t.Error("delete")
	}
	for i := 0; i < MaxFacts; i++ {
		if _, _, err := s.AddFact(ctx, uid, fmt.Sprintf("fact %d", i)); err != nil {
			if !errors.Is(err, ErrTooManyFacts) || i != MaxFacts-1 {
				t.Fatalf("fact %d: %v", i, err)
			}
		}
	}
	if n, _ := s.ClearFacts(ctx, uid); n != MaxFacts {
		t.Errorf("cleared %d", n)
	}

	if v, _ := s.UserPref(ctx, uid, PrefFactsOff); v != "" {
		t.Error("unset preference")
	}
	_ = s.SetUserPref(ctx, uid, PrefFactsOff, "1")
	_ = s.SetUserPref(ctx, uid, PrefFactsOff, "2")
	if v, _ := s.UserPref(ctx, uid, PrefFactsOff); v != "2" {
		t.Errorf("pref: %q", v)
	}
	_ = s.SetUserPref(ctx, uid, PrefFactsOff, "")
	if v, _ := s.UserPref(ctx, uid, PrefFactsOff); v != "" {
		t.Error("an empty value deletes the preference")
	}
}

func TestMigrationsFindTheirWorkDone(t *testing.T) {
	s := openTest(t, Options{})
	ctx := context.Background()

	if _, err := s.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version >= 22`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatalf("columns and tables that exist already count as applied: %v", err)
	}
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n)
	if n != len(migrations) {
		t.Errorf("all versions are recorded: %d of %d", n, len(migrations))
	}
}

func TestRetention(t *testing.T) {
	s := openTest(t, Options{})
	old := s.Now().Add(-500 * 24 * time.Hour)
	_, _, _ = s.TouchUser(bg, UserInfo{ID: 1, FirstName: "kept"})
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.ExecContext(bg, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO daily_usage (user_id, day, requests) VALUES (1, ?, 3), (1, ?, 2)`, old.Format("2006-01-02"), s.Today())
	mustExec(`INSERT INTO usage_stats (day, kind, name, count) VALUES (?, 'tool', 'x', 1)`, old.Format("2006-01-02"))
	mustExec(`INSERT INTO context_messages (user_id, role, content, tokens, created_at) VALUES (1, 'user', 'mine', 1, ?), (999, 'user', 'orphan', 1, ?)`, old, old)
	mustExec(`INSERT INTO bot_chats (chat_id, type, status, updated_at) VALUES (-5, 'group', 'left', ?), (-6, 'group', 'member', ?)`, old, old)
	removed, err := s.Retention(bg)
	if err != nil {
		t.Fatal(err)
	}
	if removed["daily_usage"] != 1 || removed["usage_stats"] != 1 || removed["context_messages"] != 1 || removed["bot_chats"] != 1 {
		t.Errorf("removed %v", removed)
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM context_messages WHERE user_id = 1`).Scan(&n)
	if n != 1 {
		t.Error("an existing user's memory stays")
	}
}

func TestProviderEffortIsKept(t *testing.T) {
	s := openTest(t, Options{SecretKey: "unit-test-secret"})
	id, err := s.AddProvider(bg, Provider{Name: "deep", BaseURL: "https://a/v1", Model: "m", Enabled: true, Effort: "ultracode"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.Provider(bg, id)
	if p.Effort != "ultracode" {
		t.Fatalf("saved: %q", p.Effort)
	}
	p.Effort = ""
	_ = s.UpdateProvider(bg, *p)
	if p, _ = s.Provider(bg, id); p.Effort != "" {
		t.Errorf("back to the model's default: %q", p.Effort)
	}
}

func TestProviderStreamAndAPIAreKept(t *testing.T) {
	s := openTest(t, Options{SecretKey: "unit-test-secret"})
	id, err := s.AddProvider(bg, Provider{Name: "gate", BaseURL: "https://a/v1", Model: "m", Enabled: true, API: "responses", NoStream: true})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.Provider(bg, id)
	if p.API != "responses" || !p.NoStream {
		t.Fatalf("saved: %+v", p)
	}
	p.NoStream = false
	_ = s.UpdateProvider(bg, *p)
	if p, _ = s.Provider(bg, id); p.NoStream || p.API != "responses" {
		t.Errorf("streaming back on: %+v", p)
	}
	if def, _ := s.AddProvider(bg, Provider{Name: "plain", BaseURL: "https://b/v1", Model: "m", Enabled: true}); def > 0 {
		if q, _ := s.Provider(bg, def); q.NoStream {
			t.Error("streaming is on unless it is turned off")
		}
	}
}
