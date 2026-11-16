// Package notelite is hearth's example app: 1 small note service whose API,
// permission checks, MCP tools, events and manifest hearth's plugins generate
// from proto/example/notes/v1/notes.proto.
//
// The app writes each note and its event in 1 SQLite transaction, so that the
// outbox holds the event exactly when the note is there. A relay publishes the
// outbox's events to a transport: the bus of this process, or lodge's broker.
package notelite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/frostyard/hearth/authz"
	"github.com/frostyard/hearth/events/outbox"

	notesv1 "github.com/frostyard/lodge/internal/hearthtest/notelite/notesv1"
)

// readCap and writeCap are the app's 2 capabilities.
const (
	readCap  = "notelite:read"
	writeCap = "notelite:write"
)

// schema is the app's table: 1 note, its folder and its title.
const schema = `CREATE TABLE IF NOT EXISTS notes (
	id     TEXT PRIMARY KEY,
	folder TEXT NOT NULL,
	title  TEXT NOT NULL
)`

// The app's statements.
const (
	insertSQL = `INSERT INTO notes (id, folder, title) VALUES (?, ?, ?)`
	selectSQL = `SELECT id, folder, title FROM notes ORDER BY id`
	readSQL   = `SELECT id, folder, title FROM notes WHERE id = ?`
	deleteSQL = `DELETE FROM notes WHERE id = ?`
)

// Open opens the app's database at path, and it makes the app's table and the
// outbox's.
//
// The database holds 1 connection: SQLite writes 1 at a time, so the app's
// transactions and the relay's deletes wait for each other, and never fail with
// a lock.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open the database %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("make the notes table: %w", err)
	}
	if err := outbox.Init(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Service is the example app: its database, and the relay to wake after a
// change that wrote an event.
type Service struct {
	// DB is the app's database.
	DB *sql.DB
	// Notify wakes the relay. nil is no wake.
	Notify func()
}

// API returns the app's API with the generated permission check of every RPC.
func (s *Service) API() notesv1.NoteServiceAPI {
	return notesv1.NewNoteServiceAuthz[*sql.Tx](s, s.inTx)
}

// inTx runs fn in 1 transaction, commits it, and then wakes the relay, which
// publishes the events that the transaction wrote.
func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin the transaction: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit the transaction: %w", err)
	}
	if s.Notify != nil {
		s.Notify()
	}
	return nil
}

// CreateNote writes 1 note, and its event, in 1 transaction. The app names the
// note: its id is a random string, not the request's.
func (s *Service) CreateNote(ctx context.Context, req *notesv1.CreateNoteRequest) (*notesv1.CreateNoteResponse, error) {
	folder := folderOf(req.GetFolder())
	var note *notesv1.Note
	err := s.inTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		id, err := newID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, insertSQL, id, folder, req.GetTitle()); err != nil {
			return fmt.Errorf("write the note: %w", err)
		}
		// The event is in the change's transaction: it is published only if
		// the note commits.
		if err := notesv1.EmitNoteCreated(ctx, outbox.Tx(tx), id, folder); err != nil {
			return err
		}
		note = &notesv1.Note{Id: id, Folder: folder, Title: req.GetTitle()}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &notesv1.CreateNoteResponse{Note: note}, nil
}

// GetNote reads 1 note, in the transaction that the generated hook opened.
func (s *Service) GetNote(ctx context.Context, req *notesv1.GetNoteRequest) (*notesv1.GetNoteResponse, error) {
	tx, err := transaction(ctx)
	if err != nil {
		return nil, err
	}
	note, err := readNote(ctx, tx, req.GetId())
	if err != nil {
		return nil, err
	}
	return &notesv1.GetNoteResponse{Note: note}, nil
}

// ListNotes reads the notes of 1 folder, and it keeps only the notes that the
// caller's grant covers: the generated check reads the request's folder, and a
// note of another folder is left out.
//
// The RPC runs outside a transaction, because no part of it reads 1 stored note
// by its id.
func (s *Service) ListNotes(ctx context.Context, req *notesv1.ListNotesRequest) (*notesv1.ListNotesResponse, error) {
	grant, err := authz.GrantFrom(ctx)
	if err != nil {
		return nil, err
	}
	folder := folderOf(req.GetFolder())
	rows, err := s.DB.QueryContext(ctx, selectSQL)
	if err != nil {
		return nil, fmt.Errorf("read the notes: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	response := &notesv1.ListNotesResponse{}
	for rows.Next() {
		note := &notesv1.Note{}
		if err := rows.Scan(&note.Id, &note.Folder, &note.Title); err != nil {
			return nil, fmt.Errorf("read a note: %w", err)
		}
		if !under(note.GetFolder(), folder) || !grant.Allows(readCap, "folder", note.GetFolder()) {
			continue
		}
		response.Notes = append(response.Notes, note)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the notes: %w", err)
	}
	return response, nil
}

// DeleteNote deletes 1 note, and it writes the note's event, in the
// transaction that the generated hook opened.
func (s *Service) DeleteNote(ctx context.Context, req *notesv1.DeleteNoteRequest) (*notesv1.DeleteNoteResponse, error) {
	tx, err := transaction(ctx)
	if err != nil {
		return nil, err
	}
	note, err := readNote(ctx, tx, req.GetId())
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, deleteSQL, note.GetId()); err != nil {
		return nil, fmt.Errorf("delete the note %q: %w", note.GetId(), err)
	}
	if err := notesv1.EmitNoteDeleted(ctx, outbox.Tx(tx), note.GetId(), note.GetFolder()); err != nil {
		return nil, err
	}
	return &notesv1.DeleteNoteResponse{}, nil
}

// AuthorizeGetNote checks the folder that 1 note is stored in, before 1 call
// reads it.
func (s *Service) AuthorizeGetNote(ctx context.Context, tx *sql.Tx, grant authz.Grant, req *notesv1.GetNoteRequest) error {
	return storedFolder(ctx, tx, grant, readCap, req.GetId())
}

// AuthorizeDeleteNote checks the folder that 1 note is stored in, before 1 call
// deletes it.
func (s *Service) AuthorizeDeleteNote(ctx context.Context, tx *sql.Tx, grant authz.Grant, req *notesv1.DeleteNoteRequest) error {
	return storedFolder(ctx, tx, grant, writeCap, req.GetId())
}

// storedFolder checks 1 capability on the folder that 1 stored note is filed
// in.
func storedFolder(ctx context.Context, tx *sql.Tx, grant authz.Grant, cap, id string) error {
	note, err := readNote(ctx, tx, id)
	if err != nil {
		return err
	}
	return grant.Check(cap, authz.Sel{Selector: "folder", Path: note.GetFolder()})
}

// readNote reads 1 note of the transaction.
func readNote(ctx context.Context, tx *sql.Tx, id string) (*notesv1.Note, error) {
	note := &notesv1.Note{}
	err := tx.QueryRowContext(ctx, readSQL, id).Scan(&note.Id, &note.Folder, &note.Title)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("no note %q", id)
	}
	if err != nil {
		return nil, fmt.Errorf("read the note %q: %w", id, err)
	}
	return note, nil
}

// transaction returns the transaction of 1 RPC that runs on an object id.
func transaction(ctx context.Context) (*sql.Tx, error) {
	tx, ok := authz.TxFrom[*sql.Tx](ctx)
	if !ok {
		return nil, fmt.Errorf("the call runs outside the app's transaction")
	}
	return tx, nil
}

// folderOf returns the canonical form of 1 folder: the Unicode NFC form of the
// path that the permission check compares a grant with. An absent folder is the
// root, and a value that is no path is returned as it came, so that no grant
// covers it.
func folderOf(folder string) string {
	if folder == "" {
		return authz.Root
	}
	cleaned, err := authz.CleanPath(folder)
	if err != nil {
		return folder
	}
	return cleaned
}

// under reports whether 1 folder is the path, or below it.
func under(folder, path string) bool {
	return folder == path || strings.HasPrefix(folder, path+"/") || path == authz.Root
}

// newID returns a random id of 16 bytes, in hex.
func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("make the note's id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
