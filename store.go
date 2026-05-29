package main

import (
	"database/sql"
	"encoding/json"
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

const schemaVotes = `
CREATE TABLE IF NOT EXISTS votes (
  item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  user_slack_id TEXT NOT NULL,
  source TEXT NOT NULL,
  signal TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (item_id, user_slack_id)
);
`

const schemaProposals = `
CREATE TABLE IF NOT EXISTS proposals (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  slack_ts TEXT NOT NULL,
  draft_json TEXT NOT NULL,
  related_tickets_json TEXT NOT NULL,
  branch TEXT NOT NULL,
  existing_ticket_key TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_proposals_item ON proposals(item_id);
`

const schemaTickets = `
CREATE TABLE IF NOT EXISTS tickets (
  proposal_id INTEGER NOT NULL REFERENCES proposals(id) ON DELETE CASCADE,
  jira_key TEXT NOT NULL,
  jira_url TEXT NOT NULL,
  action TEXT NOT NULL,
  existing_ticket_key TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (proposal_id, jira_key)
);
`

const schemaEvents = `
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  item_id INTEGER REFERENCES items(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  payload_json TEXT NOT NULL DEFAULT '{}',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_events_item ON events(item_id);
`

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schemaItems + schemaVotes + schemaProposals + schemaTickets + schemaEvents); err != nil {
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

func (s *Store) UpsertVote(itemID int64, user, source, signal string) error {
	_, err := s.db.Exec(`INSERT INTO votes(item_id, user_slack_id, source, signal) VALUES(?,?,?,?)
		ON CONFLICT(item_id, user_slack_id) DO NOTHING`, itemID, user, source, signal)
	return err
}

func (s *Store) RemoveVote(itemID int64, user string) error {
	_, err := s.db.Exec(`DELETE FROM votes WHERE item_id = ? AND user_slack_id = ?`, itemID, user)
	return err
}

func (s *Store) CountVotes(itemID int64) (int, error) {
	row := s.db.QueryRow(`SELECT COUNT(*) FROM votes WHERE item_id = ?`, itemID)
	var n int
	err := row.Scan(&n)
	return n, err
}

func (s *Store) HasVoted(itemID int64, user string) (bool, error) {
	row := s.db.QueryRow(`SELECT 1 FROM votes WHERE item_id = ? AND user_slack_id = ?`, itemID, user)
	var v int
	err := row.Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

type Proposal struct {
	ID                 int64
	ItemID             int64
	SlackTS            string
	DraftJSON          string
	RelatedTicketsJSON string
	Branch             string
	ExistingTicketKey  string
	Status             string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type Ticket struct {
	ProposalID        int64
	JiraKey           string
	JiraURL           string
	Action            string
	ExistingTicketKey string
	CreatedAt         time.Time
}

type Event struct {
	ID        int64
	ItemID    *int64
	Kind      string
	Payload   string
	CreatedAt time.Time
}

func (s *Store) InsertProposal(p *Proposal) error {
	res, err := s.db.Exec(`INSERT INTO proposals(item_id, slack_ts, draft_json, related_tickets_json, branch, existing_ticket_key, status) VALUES(?,?,?,?,?,?,?)`,
		p.ItemID, p.SlackTS, p.DraftJSON, p.RelatedTicketsJSON, p.Branch, p.ExistingTicketKey, p.Status)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	p.ID = id
	return nil
}

func (s *Store) GetLatestProposal(itemID int64) (*Proposal, error) {
	row := s.db.QueryRow(`SELECT id, item_id, slack_ts, draft_json, related_tickets_json, branch, existing_ticket_key, status, created_at, updated_at FROM proposals WHERE item_id = ? ORDER BY id DESC LIMIT 1`, itemID)
	var p Proposal
	err := row.Scan(&p.ID, &p.ItemID, &p.SlackTS, &p.DraftJSON, &p.RelatedTicketsJSON, &p.Branch, &p.ExistingTicketKey, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &p, err
}

func (s *Store) UpdateProposalStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE proposals SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, status, id)
	return err
}

func (s *Store) GetProposalBySlackTS(ts string) (*Proposal, error) {
	row := s.db.QueryRow(`SELECT id, item_id, slack_ts, draft_json, related_tickets_json, branch, existing_ticket_key, status, created_at, updated_at FROM proposals WHERE slack_ts = ?`, ts)
	var p Proposal
	err := row.Scan(&p.ID, &p.ItemID, &p.SlackTS, &p.DraftJSON, &p.RelatedTicketsJSON, &p.Branch, &p.ExistingTicketKey, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &p, err
}

func (s *Store) InsertTicket(t *Ticket) error {
	_, err := s.db.Exec(`INSERT INTO tickets(proposal_id, jira_key, jira_url, action, existing_ticket_key) VALUES(?,?,?,?,?)`,
		t.ProposalID, t.JiraKey, t.JiraURL, t.Action, t.ExistingTicketKey)
	return err
}

func (s *Store) GetTicketByProposal(proposalID int64) (*Ticket, error) {
	row := s.db.QueryRow(`SELECT proposal_id, jira_key, jira_url, action, existing_ticket_key, created_at FROM tickets WHERE proposal_id = ? LIMIT 1`, proposalID)
	var t Ticket
	err := row.Scan(&t.ProposalID, &t.JiraKey, &t.JiraURL, &t.Action, &t.ExistingTicketKey, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &t, err
}

func (s *Store) LogEvent(itemID *int64, kind, payload string) error {
	_, err := s.db.Exec(`INSERT INTO events(item_id, kind, payload_json) VALUES(?,?,?)`, itemID, kind, payload)
	return err
}

func (s *Store) ListEventsForItem(itemID int64) ([]*Event, error) {
	rows, err := s.db.Query(`SELECT id, item_id, kind, payload_json, created_at FROM events WHERE item_id = ? ORDER BY id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Event
	for rows.Next() {
		var e Event
		var iid sql.NullInt64
		if err := rows.Scan(&e.ID, &iid, &e.Kind, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		if iid.Valid {
			v := iid.Int64
			e.ItemID = &v
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

func (s *Store) UpdateItemReminderTimes(id int64, lastReminder, warning *time.Time) error {
	_, err := s.db.Exec(`UPDATE items SET last_reminder_at = ?, warning_posted_at = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, lastReminder, warning, id)
	return err
}

func (s *Store) UpdateItemSubproject(id int64, sub string) error {
	_, err := s.db.Exec(`UPDATE items SET subproject = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, sub, id)
	return err
}

func (s *Store) UpdateItemText(id int64, text string) error {
	_, err := s.db.Exec(`UPDATE items SET text = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, text, id)
	return err
}

func (s *Store) UpdateProposalBranch(id int64, branch, status string) error {
	_, err := s.db.Exec(`UPDATE proposals SET branch = ?, status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, branch, status, id)
	return err
}

func (s *Store) GetLatestProposalByID(id int64) (*Proposal, error) {
	row := s.db.QueryRow(`SELECT id, item_id, slack_ts, draft_json, related_tickets_json, branch, existing_ticket_key, status, created_at, updated_at FROM proposals WHERE id = ?`, id)
	var p Proposal
	err := row.Scan(&p.ID, &p.ItemID, &p.SlackTS, &p.DraftJSON, &p.RelatedTicketsJSON, &p.Branch, &p.ExistingTicketKey, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &p, err
}

func (s *Store) LatestOverride(itemID int64, kind string) (string, error) {
	row := s.db.QueryRow(`SELECT payload_json FROM events WHERE item_id = ? AND kind = ? ORDER BY id DESC LIMIT 1`, itemID, kind)
	var p string
	err := row.Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// payload is {"value":"...", "by":"..."}
	var parsed struct {
		Value string `json:"value"`
	}
	_ = json.Unmarshal([]byte(p), &parsed)
	return parsed.Value, nil
}
