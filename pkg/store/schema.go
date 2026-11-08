package store

var migrations = [][]string{

	{

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

		`CREATE TABLE IF NOT EXISTS daily_usage (
			user_id INTEGER NOT NULL,
			day DATE NOT NULL,
			requests INTEGER NOT NULL DEFAULT 0,
			tokens INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (user_id, day)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_day ON daily_usage (day)`,

		`CREATE TABLE IF NOT EXISTS context_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			tokens INTEGER NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ctx_user ON context_messages (user_id, id)`,

		`CREATE TABLE IF NOT EXISTS context_memory (
			user_id INTEGER NOT NULL PRIMARY KEY,
			summary TEXT NOT NULL,
			summary_tokens INTEGER NOT NULL DEFAULT 0,
			compressions INTEGER NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL
		)`,

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

		`CREATE TABLE IF NOT EXISTS settings (
			k TEXT NOT NULL PRIMARY KEY,
			v TEXT NOT NULL,
			updated_at DATETIME NOT NULL
		)`,

		`CREATE TABLE IF NOT EXISTS admins (
			user_id INTEGER NOT NULL PRIMARY KEY,
			added_by INTEGER NOT NULL,
			created_at DATETIME NOT NULL
		)`,

		`CREATE TABLE IF NOT EXISTS premium_emoji (
			emoji TEXT NOT NULL PRIMARY KEY,
			custom_emoji_id TEXT NOT NULL,
			created_at DATETIME NOT NULL
		)`,

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

		`CREATE TABLE IF NOT EXISTS user_facts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			fact TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_facts_user ON user_facts (user_id, id)`,

		`CREATE TABLE IF NOT EXISTS user_prefs (
			user_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			value TEXT NOT NULL,
			PRIMARY KEY (user_id, name)
		)`,

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

		`CREATE TABLE IF NOT EXISTS chat_prefs (
			chat_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			value TEXT NOT NULL,
			PRIMARY KEY (chat_id, name)
		)`,

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

		`CREATE TABLE IF NOT EXISTS user_cookies (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			label TEXT NOT NULL,
			domains TEXT NOT NULL DEFAULT '',
			data TEXT NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_user_cookies_user ON user_cookies (user_id)`,

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

		`CREATE TABLE IF NOT EXISTS learn_daily (
			day DATE NOT NULL,
			kind TEXT NOT NULL,
			tool TEXT NOT NULL DEFAULT '',
			n INTEGER NOT NULL DEFAULT 0,
			note TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (day, kind, tool)
		)`,

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

	{
		`DROP INDEX IF EXISTS idx_snap_log_at`,
		`DROP TABLE IF EXISTS snap_log`,
		`INSERT OR IGNORE INTO settings (k, v, updated_at) SELECT 'triage_trim', v, updated_at FROM settings WHERE k = 'snap_trim'`,
		`INSERT OR IGNORE INTO settings (k, v, updated_at) SELECT 'triage_chat', v, updated_at FROM settings WHERE k = 'snap_chat'`,
		`INSERT OR IGNORE INTO settings (k, v, updated_at) SELECT 'triage', 'local', updated_at FROM settings WHERE k = 'snap_on' AND v = 'true'`,
		`DELETE FROM settings WHERE k IN ('snap_on', 'snap_key', 'snap_model', 'snap_timeout_ms', 'snap_route', 'snap_effort', 'snap_chat', 'snap_trim')`,
	},

	{
		`CREATE TABLE IF NOT EXISTS lesson_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			lesson_id INTEGER NOT NULL DEFAULT 0,
			category TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			before TEXT NOT NULL DEFAULT '',
			after TEXT NOT NULL DEFAULT '',
			note TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_lesson_log_lesson ON lesson_log (lesson_id, id)`,
	},

	{
		`CREATE TABLE IF NOT EXISTS cmd_allow (
			pattern TEXT NOT NULL PRIMARY KEY,
			created_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS cmd_audit (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			at DATETIME NOT NULL,
			session TEXT NOT NULL DEFAULT '',
			origin TEXT NOT NULL DEFAULT '',
			command TEXT NOT NULL,
			decision TEXT NOT NULL,
			decider TEXT NOT NULL DEFAULT '',
			rule TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cmd_audit_at ON cmd_audit (at)`,
	},

	{
		`CREATE TABLE IF NOT EXISTS kanban (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			title TEXT NOT NULL,
			notes TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'todo',
			bg_task INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_kanban_user ON kanban (user_id, status)`,
	},
}
