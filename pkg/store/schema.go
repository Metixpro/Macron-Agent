package store

// The database schema, in SQLite's own SQL.
//
// Times are DATETIME / DATE columns holding UTC text of a fixed width (see
// timeLayout in sqlite.go), so a comparison of the text is a comparison of the
// times, and the driver hands them back as time.Time. Flags are INTEGER 0/1.
//
// Each entry of migrations is one version of the database; a database that is
// behind runs the entries it has not seen, each statement on its own (see
// migrate). A change to a database that exists is a new entry at the end: an
// ALTER TABLE … ADD COLUMN, a new table, a new index. Never edit an entry that
// has shipped.
var migrations = [][]string{
	// 1: the whole schema
	{
		// the people who talked to the agent (the owner, and anyone the owner allows)
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER NOT NULL PRIMARY KEY,
			username TEXT COLLATE NOCASE NULL,
			first_name TEXT NOT NULL DEFAULT '',
			last_name TEXT NOT NULL DEFAULT '',
			language_code TEXT NOT NULL DEFAULT '',
			is_premium INTEGER NOT NULL DEFAULT 0,
			bio TEXT NULL,
			birthdate TEXT NOT NULL DEFAULT '',
			profile_checked_at DATETIME NULL,
			blocked_bot INTEGER NOT NULL DEFAULT 0,
			messages_total INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			last_seen_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_users_username ON users (username)`,
		`CREATE INDEX IF NOT EXISTS idx_users_last_seen ON users (last_seen_at)`,
		`CREATE INDEX IF NOT EXISTS idx_users_created ON users (created_at)`,

		// the requests and tokens each user spends a day
		`CREATE TABLE IF NOT EXISTS daily_usage (
			user_id INTEGER NOT NULL,
			day DATE NOT NULL,
			requests INTEGER NOT NULL DEFAULT 0,
			tokens INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (user_id, day)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_day ON daily_usage (day)`,

		// the conversation window
		`CREATE TABLE IF NOT EXISTS context_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			tokens INTEGER NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ctx_user ON context_messages (user_id, id)`,

		// the summary of what the window forgot
		`CREATE TABLE IF NOT EXISTS context_memory (
			user_id INTEGER NOT NULL PRIMARY KEY,
			summary TEXT NOT NULL,
			summary_tokens INTEGER NOT NULL DEFAULT 0,
			compressions INTEGER NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL
		)`,

		// the models the agent talks to
		`CREATE TABLE IF NOT EXISTS providers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT COLLATE NOCASE NOT NULL,
			base_url TEXT NOT NULL,
			api_key TEXT NOT NULL,
			model TEXT NOT NULL,
			priority INTEGER NOT NULL DEFAULT 100,
			enabled INTEGER NOT NULL DEFAULT 1,
			vision INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL,
			last_ok_at DATETIME NULL,
			last_error TEXT NOT NULL DEFAULT '',
			last_error_at DATETIME NULL,
			requests INTEGER NOT NULL DEFAULT 0,
			failures INTEGER NOT NULL DEFAULT 0,
			avg_latency_ms INTEGER NOT NULL DEFAULT 0,
			api TEXT NOT NULL DEFAULT '',            -- the API it speaks: '' (from its address), openai, anthropic
			context_window INTEGER NOT NULL DEFAULT 0, -- its own window, for a model whose name says nothing reliable
			temperature REAL NULL,                   -- NULL: the default
			effort TEXT NOT NULL DEFAULT '',         -- how hard it thinks: '' is its own default
			no_stream INTEGER NOT NULL DEFAULT 0     -- 1: no streaming, for a gateway that breaks it
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_provider_name ON providers (name)`,

		// settings
		`CREATE TABLE IF NOT EXISTS settings (
			k TEXT NOT NULL PRIMARY KEY,
			v TEXT NOT NULL,
			updated_at DATETIME NOT NULL
		)`,

		// admins
		`CREATE TABLE IF NOT EXISTS admins (
			user_id INTEGER NOT NULL PRIMARY KEY,
			added_by INTEGER NOT NULL,
			created_at DATETIME NOT NULL
		)`,

		// premium emoji (the keys compare byte for byte)
		`CREATE TABLE IF NOT EXISTS premium_emoji (
			emoji TEXT NOT NULL PRIMARY KEY,
			custom_emoji_id TEXT NOT NULL,
			created_at DATETIME NOT NULL
		)`,

		// statistics
		`CREATE TABLE IF NOT EXISTS usage_stats (
			day DATE NOT NULL,
			kind TEXT NOT NULL,
			name TEXT NOT NULL,
			count INTEGER NOT NULL DEFAULT 0,
			failures INTEGER NOT NULL DEFAULT 0,
			prompt_tokens INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (day, kind, name)
		)`,

		// groups and channels the agent is in
		`CREATE TABLE IF NOT EXISTS bot_chats (
			chat_id INTEGER NOT NULL PRIMARY KEY,
			type TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			username TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			members INTEGER NOT NULL DEFAULT 0,
			added_by INTEGER NOT NULL DEFAULT 0,
			added_by_name TEXT NOT NULL DEFAULT '',
			joined_at DATETIME NULL,
			left_at DATETIME NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_chats_status ON bot_chats (status, updated_at)`,

		// questions the agent put to a user as a poll
		`CREATE TABLE IF NOT EXISTS polls (
			poll_id TEXT NOT NULL PRIMARY KEY,
			user_id INTEGER NOT NULL,
			chat_id INTEGER NOT NULL,
			chat_type TEXT NOT NULL,
			message_id INTEGER NOT NULL,
			question TEXT NOT NULL,
			options TEXT NOT NULL,
			multiple INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			answered_at DATETIME NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_polls_created ON polls (created_at)`,

		// reminders and scheduled tasks
		`CREATE TABLE IF NOT EXISTS schedules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			chat_id INTEGER NOT NULL,
			chat_type TEXT NOT NULL,
			kind TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			body TEXT NOT NULL,
			rule TEXT NOT NULL DEFAULT '',
			next_run DATETIME NOT NULL,
			last_run DATETIME NULL,
			runs INTEGER NOT NULL DEFAULT 0,
			failures INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'active',
			last_error TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			thread_id INTEGER NOT NULL DEFAULT 0     -- the forum topic it posts to
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sched_due ON schedules (status, next_run)`,
		`CREATE INDEX IF NOT EXISTS idx_sched_user ON schedules (user_id, status)`,

		// long-term memory (facts the user can see and delete)
		`CREATE TABLE IF NOT EXISTS user_facts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			fact TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_facts_user ON user_facts (user_id, id)`,

		// per-user preferences
		`CREATE TABLE IF NOT EXISTS user_prefs (
			user_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			value TEXT NOT NULL,
			PRIMARY KEY (user_id, name)
		)`,

		// group messages kept for a while for catch-up summaries (opt-in per group)
		`CREATE TABLE IF NOT EXISTS group_messages (
			chat_id INTEGER NOT NULL,
			message_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			username TEXT COLLATE NOCASE NOT NULL DEFAULT '',
			reply_to INTEGER NOT NULL DEFAULT 0,
			text TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			expires_at DATETIME NOT NULL,
			PRIMARY KEY (chat_id, message_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_gm_expires ON group_messages (expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_gm_chat_time ON group_messages (chat_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_gm_user ON group_messages (chat_id, user_id, message_id)`,

		// per-chat preferences
		`CREATE TABLE IF NOT EXISTS chat_prefs (
			chat_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			value TEXT NOT NULL,
			PRIMARY KEY (chat_id, name)
		)`,

		// the downloaders' cookie pool (logins for YouTube, Instagram…)
		`CREATE TABLE IF NOT EXISTS dl_cookies (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			label TEXT NOT NULL,
			domains TEXT NOT NULL DEFAULT '',
			data TEXT NOT NULL,
			rest_until DATETIME NULL,
			last_used DATETIME NULL,
			uses INTEGER NOT NULL DEFAULT 0,
			blocks INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			source_url TEXT NOT NULL DEFAULT '',     -- cookies fetched from a link are fetched again when they stop working
			fetched_at DATETIME NULL
		)`,

		// each user's own cookies, used only for their downloads
		`CREATE TABLE IF NOT EXISTS user_cookies (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			label TEXT NOT NULL,
			domains TEXT NOT NULL DEFAULT '',
			data TEXT NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_user_cookies_user ON user_cookies (user_id)`,

		// lessons the agent drew from its own jobs, waiting for the owner
		`CREATE TABLE IF NOT EXISTS lessons (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			lesson_key TEXT NOT NULL,
			category TEXT NOT NULL DEFAULT 'general',
			title TEXT NOT NULL,
			rule TEXT NOT NULL,
			tools TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			seen INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			decided_at DATETIME NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_lesson_key ON lessons (lesson_key)`,
		`CREATE INDEX IF NOT EXISTS idx_lessons_status ON lessons (status, updated_at)`,

		// how the agent did each day (jobs, feedback, tool calls and failures), for the weekly report
		`CREATE TABLE IF NOT EXISTS learn_daily (
			day DATE NOT NULL,
			kind TEXT NOT NULL,
			tool TEXT NOT NULL DEFAULT '',
			n INTEGER NOT NULL DEFAULT 0,
			note TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (day, kind, tool)
		)`,

		// the agent's periodic exams: a fixed set of hard requests, graded
		`CREATE TABLE IF NOT EXISTS eval_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			started_at DATETIME NOT NULL,
			took_ms INTEGER NOT NULL DEFAULT 0,
			reason TEXT NOT NULL DEFAULT '',
			provider TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			passed INTEGER NOT NULL DEFAULT 0,
			total INTEGER NOT NULL DEFAULT 0,
			details TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_eval_runs_started ON eval_runs (started_at)`,

		// the job each user has running, and the last one that stopped before
		// it was done (see openjobs.go): a restart or an error never loses it
		`CREATE TABLE IF NOT EXISTS open_jobs (
			user_id INTEGER NOT NULL,
			kind TEXT NOT NULL,
			chat_id INTEGER NOT NULL,
			msg_id INTEGER NOT NULL DEFAULT 0,
			request TEXT NOT NULL,
			notes TEXT NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			later INTEGER NOT NULL DEFAULT 0,
			started_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			PRIMARY KEY (user_id, kind)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_open_jobs_kind ON open_jobs (kind, updated_at)`,

		// background tasks (see bgtasks.go): long jobs that run on their own
		// while the owner goes on chatting
		`CREATE TABLE IF NOT EXISTS bg_tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			chat_id INTEGER NOT NULL,
			chat_type TEXT NOT NULL,
			thread_id INTEGER NOT NULL DEFAULT 0,
			msg_id INTEGER NOT NULL DEFAULT 0,
			title TEXT NOT NULL,
			goal TEXT NOT NULL,
			status TEXT NOT NULL,
			note TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			started_at DATETIME NULL,
			finished_at DATETIME NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_bg_tasks_user ON bg_tasks (user_id, id)`,
		`CREATE INDEX IF NOT EXISTS idx_bg_tasks_status ON bg_tasks (status)`,

		// the fast classifier's decisions (see snap.go): figures only, never
		// a message or who sent it
		`CREATE TABLE IF NOT EXISTS snap_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			at DATETIME NOT NULL,
			tier TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL DEFAULT '',
			kind_conf REAL NOT NULL DEFAULT 0,
			needs_tools REAL NOT NULL DEFAULT 0,
			work REAL NOT NULL DEFAULT 0,
			context TEXT NOT NULL DEFAULT '',
			applied TEXT NOT NULL DEFAULT '',
			latency_ms INTEGER NOT NULL DEFAULT 0,
			snap_tokens INTEGER NOT NULL DEFAULT 0,
			saved_tokens INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_snap_log_at ON snap_log (at)`,

		// every exchange, kept for good and searchable (full text): the
		// conversation window forgets and summarizes, the archive does not
		`CREATE TABLE IF NOT EXISTS chat_archive (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_archive_user ON chat_archive (user_id, id)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS chat_archive_fts USING fts5(content, content='chat_archive', content_rowid='id', tokenize='unicode61 remove_diacritics 2')`,
		`CREATE TRIGGER IF NOT EXISTS chat_archive_ai AFTER INSERT ON chat_archive BEGIN
			INSERT INTO chat_archive_fts(rowid, content) VALUES (new.id, new.content);
		END`,
		`CREATE TRIGGER IF NOT EXISTS chat_archive_ad AFTER DELETE ON chat_archive BEGIN
			INSERT INTO chat_archive_fts(chat_archive_fts, rowid, content) VALUES ('delete', old.id, old.content);
		END`,
	},
}
