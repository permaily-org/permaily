package store

import (
	"context"
	"errors"
)

// A message's parts written and read one at a time (ADR 0026, "Delivery" and
// "The inbox"). Store.PutMessage writes the raw object and the item in one
// call and Store.GetMessage reads both, the raw object whole; delivery needs
// the parts apart, in its order, and reading needs the item without the raw
// object and the raw object bounded. *Memory and *AWS implement MessageParts
// from pull request 0, which also changes Store.DeleteMessage to delete the
// raw object first (below). Nothing implements it before.

// ErrTooLarge is returned by MessageParts.ReadRaw when the raw object holds
// more than the caller allowed: nothing past the limit was kept.
var ErrTooLarge = errors.New("store: larger than the caller allows")

// maxRawRead is the largest bound ReadRaw takes: the largest message the
// service accepts (25 MiB, inbox.MaxMessageBytes) and its seal, rounded up.
// A bound above it is a mistake of the caller's, and an error.
const maxRawRead = 32 << 20

// MessageParts is the parts of a stored message.
type MessageParts interface {
	// PutRaw writes the message's sealed raw object (raw/<mailbox>/<id>,
	// tagged with m.RetentionDays) only if nothing is at its key: ErrExists
	// when an earlier try of the same delivery wrote it. S3 stores an object
	// whole or not at all, so one that is there is complete, and it keeps
	// the date of its first write, which the lifecycle rule counts from.
	PutRaw(ctx context.Context, m *Message, raw []byte) error
	// PutItem writes the message item (pk=mailbox#<id>, sk=msg#<id>) with
	// every field of m, replacing an earlier try's: the claim fixes those
	// fields, so a retry writes the same ones.
	PutItem(ctx context.Context, m *Message) error
	// Item reads the message item alone, never the raw object: ErrNotFound
	// when it is not there or has expired.
	Item(ctx context.Context, mailboxID, id string) (*Message, error)
	// ReadRaw reads the sealed raw object and stops with ErrTooLarge once
	// more than max bytes come, whatever the item's Size says (whoever
	// writes to the table sets it): ErrNotFound when it is not there. A max
	// below zero or above the largest sealed message the service accepts
	// (32 MiB) is an error before any request.
	ReadRaw(ctx context.Context, mailboxID, id string, max int64) ([]byte, error)
}

// From pull request 0, Store.DeleteMessage deletes the raw object first and
// the item after, and may be called again after any failure: deleting a raw
// object that is not there is not an error, so a retry finishes a delete
// that stopped between the two. It returns ErrNotFound only when the item
// was not there (the raw object was deleted all the same). Until then it
// deletes the item first, and a delete that failed at the raw object leaves
// it behind and answers ErrNotFound when asked again.

// Step is one request to the store, as Memory.Interrupt names it.
// Pull request 0 adds to Memory
//
//	// Interrupt, when set, is called before each read, write and delete
//	// with what it is about to do; an error it returns fails that one, as a
//	// failure of a request to DynamoDB or S3 would, and leaves every
//	// earlier one done. It may block, as a slow request would: the store
//	// method returns ctx.Err() when ctx ends first. PutMessage is two
//	// steps (StepRaw, then StepItem), as it is two requests on AWS.
//	Interrupt func(ctx context.Context, step Step, partition, key string) error
//
// so that tests stop a delivery, a deletion or a sweep between any two of
// its requests, or hold one past a deadline.
type Step string

// The steps Interrupt sees.
const (
	StepRaw         Step = "raw"          // PutRaw, and the first half of PutMessage
	StepItem        Step = "item"         // PutItem, and the second half of PutMessage
	StepEntry       Step = "entry"        // PutEntry
	StepDeleteRaw   Step = "delete-raw"   // the first half of DeleteMessage
	StepDeleteItem  Step = "delete-item"  // the second half of DeleteMessage
	StepDeleteEntry Step = "delete-entry" // DeleteEntry, DeleteEntryAt, each entry of DeletePartition
	StepRecord      Step = "record"       // PutRecord, DeleteRecord
	StepRead        Step = "read"         // every read: records, entries, items, raw objects, scans
)
