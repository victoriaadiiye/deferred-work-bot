package main

import (
	"database/sql"
	"errors"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type Item struct {
	ID                int64
	SlackChannel      string
	SlackTS           string
	AuthorSlackID     string
	Text              string
	Subproject        string
	Status            string
	ApprovalThreshold int
	LastReminderAt    *time.Time
	WarningPostedAt   *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

const schemaItems = `
CREATE TABLE IF NOT EXISTS items (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  slack_channel TEXT NOT NULL,
  slack_ts TEXT NOT NULL UNIQUE,
  author_slack_id TEXT NOT NULL,
  text TEXT NOT NULL,
  subproject TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  approval_threshold INTEGER NOT NULL DEFAULT 3,
  last_reminder_at TIMESTAMP,
  warning_posted_at TIMESTAMP,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_items_status ON items(status);
`

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schemaItems); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) InsertItem(it *Item) error {
	res, err := s.db.Exec(`INSERT INTO items(slack_channel, slack_ts, author_slack_id, text, subproject, status, approval_threshold) VALUES(?,?,?,?,?,?,?)`,
		it.SlackChannel, it.SlackTS, it.AuthorSlackID, it.Text, it.Subproject, it.Status, it.ApprovalThreshold)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	it.ID = id
	return s.refreshItem(it)
}

func (s *Store) refreshItem(it *Item) error {
	got, err := s.GetItemByID(it.ID)
	if err != nil {
		return err
	}
	*it = *got
	return nil
}

func (s *Store) GetItemByID(id int64) (*Item, error) {
	row := s.db.QueryRow(`SELECT id, slack_channel, slack_ts, author_slack_id, text, subproject, status, approval_threshold, last_reminder_at, warning_posted_at, created_at, updated_at FROM items WHERE id = ?`, id)
	return scanItem(row)
}

func (s *Store) GetItemByTS(channel, ts string) (*Item, error) {
	row := s.db.QueryRow(`SELECT id, slack_channel, slack_ts, author_slack_id, text, subproject, status, approval_threshold, last_reminder_at, warning_posted_at, created_at, updated_at FROM items WHERE slack_channel = ? AND slack_ts = ?`, channel, ts)
	return scanItem(row)
}

func (s *Store) UpdateItemStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE items SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, status, id)
	return err
}

func (s *Store) ListItemsByStatus(status string) ([]*Item, error) {
	rows, err := s.db.Query(`SELECT id, slack_channel, slack_ts, author_slack_id, text, subproject, status, approval_threshold, last_reminder_at, warning_posted_at, created_at, updated_at FROM items WHERE status = ? ORDER BY created_at`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Item
	for rows.Next() {
		it, err := scanItemRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanItem(r rowScanner) (*Item, error) {
	var it Item
	var lastRem, warnAt sql.NullTime
	err := r.Scan(&it.ID, &it.SlackChannel, &it.SlackTS, &it.AuthorSlackID, &it.Text, &it.Subproject, &it.Status, &it.ApprovalThreshold, &lastRem, &warnAt, &it.CreatedAt, &it.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if lastRem.Valid {
		it.LastReminderAt = &lastRem.Time
	}
	if warnAt.Valid {
		it.WarningPostedAt = &warnAt.Time
	}
	return &it, nil
}

func scanItemRows(r *sql.Rows) (*Item, error) { return scanItem(r) }

var ErrNotFound = errors.New("not found")
