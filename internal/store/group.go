package store

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Groups of strict records (ADR 0037, "Where a link is kept"). A series of
// records made once and numbered, such as an account's links, keeps its
// commits together: each record is a member of its group, members in their
// order, so one listing of the group from a member reads the full history
// of every member above it, forks and late versions included, a thousand
// objects a request. S3Commits.Group and MemoryCommits.Group say which names
// are grouped and how.

// Member is one member of a group as a listing of the group read it: its
// member (as Group names it), how many object versions and delete markers
// were listed under it, and its commits. History.Head is nil when none of
// what was listed is a commit of this deployment's for a name of that
// member: its commits' lock ended and they were deleted, or only something
// else is there. A member with nothing listed is not reported.
type Member struct {
	Member  string
	Objects int
	History *History
}

// ErrListedMax is a read of a range that listed more objects than it was
// allowed before the history of its last member was complete: what was
// read is not the range's, and the caller reads a smaller one.
var ErrListedMax error = &kind{msg: "store: more objects where a range's commits are than the read may list"}

// RangeReader lists the histories of a group's members. S3Commits and
// MemoryCommits implement it once their Group is set.
type RangeReader interface {
	// HistoryRange calls each, in their order, with every member of from's
	// group from from's member up to to's (to "" for the end of the group)
	// that has anything listed, once every object version and delete marker
	// under it was listed and read. maxListed above zero bounds what is
	// listed of the range: past it, the read stops with ErrListedMax, each
	// having been called with the members complete before. An error each
	// returns stops the read and is returned.
	HistoryRange(ctx context.Context, from, to string, maxListed int, each func(Member) error) error
}

// rangeOf is the group and members of a range from from's member to to's
// ("" for the end).
func rangeOf(group func(string) (string, string, bool), from, to string) (g, first, last string, err error) {
	if group == nil {
		return "", "", "", errors.New("store: a range of commits needs a group")
	}
	g, first, ok := group(from)
	if !ok {
		return "", "", "", errors.New("store: a range of commits of a name no group holds")
	}
	if to != "" {
		g2, m, ok := group(to)
		if !ok || g2 != g || m < first {
			return "", "", "", errors.New("store: a range of commits ends in its group, at or after its start")
		}
		last = m
	}
	return g, first, last, nil
}

// listedMember is a member a listing is going through.
type listedMember struct {
	member  string
	count   int
	objects []historyObject
}

// HistoryRange implements RangeReader. The group's objects are listed from
// the range's first member up, one request a thousand objects, both of
// S3's markers carried from one to the next (a key whose versions S3
// returns in two responses is one member's, complete once a later key or
// the end is listed), a marker that does not move on refused. Each object
// is read as History reads one, and counts only if it is a commit of this
// deployment whose name the group puts at that member.
func (s *S3Commits) HistoryRange(ctx context.Context, from, to string, maxListed int, each func(Member) error) error {
	group, first, last, err := rangeOf(s.Group, from, to)
	if err != nil {
		return err
	}
	key, err := s.Key(ctx)
	if err != nil {
		return err
	}
	prefix := groupPrefix(group)
	cacheName := "\x00" + group
	var cur *listedMember
	flush := func() error {
		m := cur
		cur = nil
		if m == nil {
			return nil
		}
		want := func(name string) bool {
			g, member, ok := s.Group(name)
			return ok && g == group && member == m.member
		}
		h, err := s.historyOf(ctx, key, cacheName, want, m.objects)
		if err != nil {
			return err
		}
		return each(Member{Member: m.member, Objects: m.count, History: h})
	}
	in := &s3.ListObjectVersionsInput{Bucket: aws.String(s.Bucket), Prefix: aws.String(prefix), KeyMarker: aws.String(prefix + first)}
	listed := 0
	for {
		out, err := s.S3.ListObjectVersions(ctx, in)
		if err != nil {
			return fmt.Errorf("store: list commits: %w", err)
		}
		type entry struct {
			key, id string
			marker  bool
		}
		page := make([]entry, 0, len(out.Versions)+len(out.DeleteMarkers))
		for _, v := range out.Versions {
			page = append(page, entry{aws.ToString(v.Key), aws.ToString(v.VersionId), false})
		}
		for _, d := range out.DeleteMarkers {
			page = append(page, entry{aws.ToString(d.Key), aws.ToString(d.VersionId), true})
		}
		slices.SortStableFunc(page, func(x, y entry) int { return cmp.Compare(x.key, y.key) })
		for _, e := range page {
			member, rest, _ := strings.Cut(strings.TrimPrefix(e.key, prefix), "/")
			if last != "" && member > last {
				return flush()
			}
			listed++
			if maxListed > 0 && listed > maxListed {
				return ErrListedMax
			}
			if cur == nil || cur.member != member {
				if err := flush(); err != nil {
					return err
				}
				cur = &listedMember{member: member}
			}
			cur.count++
			n, err := strconv.ParseUint(rest, 10, 64)
			if e.marker || err != nil || rest != fmt.Sprintf("%020d", n) || n == 0 {
				continue
			}
			cur.objects = append(cur.objects, historyObject{n, e.key, e.id})
		}
		if !aws.ToBool(out.IsTruncated) {
			return flush()
		}
		if out.NextKeyMarker == nil || (aws.ToString(out.NextKeyMarker) == aws.ToString(in.KeyMarker) && aws.ToString(out.NextVersionIdMarker) == aws.ToString(in.VersionIdMarker)) {
			return errors.New("store: the listing of a group's commits does not move on")
		}
		in.KeyMarker, in.VersionIdMarker = out.NextKeyMarker, out.NextVersionIdMarker
	}
}

// HistoryRange implements RangeReader: every commit, copy and object that
// is not a commit (Take) of a name its Group puts in the range counts as
// one object listed.
func (m *MemoryCommits) HistoryRange(_ context.Context, from, to string, maxListed int, each func(Member) error) error {
	group, first, last, err := rangeOf(m.Group, from, to)
	if err != nil {
		return err
	}
	type found struct {
		objects int
		copies  []commitCopy
	}
	members := map[string]*found{}
	at := func(name string) *found {
		g, member, ok := m.Group(name)
		if !ok || g != group || member < first || (last != "" && member > last) {
			return nil
		}
		if members[member] == nil {
			members[member] = &found{}
		}
		return members[member]
	}
	m.mu.Lock()
	for name, list := range m.commits {
		if f := at(name); f != nil {
			for _, c := range list {
				f.objects++
				f.copies = append(f.copies, commitCopy{c, true})
			}
		}
	}
	for name, list := range m.copies {
		if f := at(name); f != nil {
			f.objects += len(list)
			f.copies = append(f.copies, list...)
		}
	}
	for name, versions := range m.taken {
		if f := at(name); f != nil {
			f.objects += len(versions)
		}
	}
	m.mu.Unlock()
	order := make([]string, 0, len(members))
	for member, f := range members {
		if f.objects > 0 {
			order = append(order, member)
		}
	}
	slices.Sort(order)
	listed := 0
	for _, member := range order {
		f := members[member]
		listed += f.objects
		if maxListed > 0 && listed > maxListed {
			return ErrListedMax
		}
		if err := each(Member{Member: member, Objects: f.objects, History: history(f.copies)}); err != nil {
			return err
		}
	}
	return nil
}

// ReadRange lists a range of a group's strict records, as HistoryRange
// does, from one listing: from's member up to to's ("" for the end). A fork
// or an unlocked commit found is reported as GetRecordAt reports one (a
// Regression); a member whose history holds no commit is given as such,
// never left out. The values are read with ReadListed, for the members the
// caller needs. It reads only strictly, from a store of commits that lists
// groups, and its errors hold no name.
func (a *Anchored) ReadRange(ctx context.Context, from, to string, maxListed int, each func(Member) error) error {
	reader, ok := a.Commits.(RangeReader)
	if !a.Strict || !ok {
		return errors.New("store: a range is read strictly, from a store of commits that lists groups")
	}
	return reader.HistoryRange(ctx, from, to, maxListed, func(m Member) error {
		if h := m.History; h.Head != nil {
			a.mu.Lock()
			if len(h.Forks) > 0 {
				a.alarm(Regression{Name: h.Head.Name, Found: h.Forks[0].Version, Fork: true})
			}
			if len(h.Unlocked) > 0 {
				a.alarm(Regression{Name: h.Head.Name, Found: h.Unlocked[0], Unlocked: true})
			}
			a.mu.Unlock()
		}
		return each(m)
	})
}

// ReadListed reads the value of a member ReadRange listed, as GetRecord
// reads a strict record by its history: ErrNotFound when it holds no
// commit, ErrForked when it has a fork, ErrLost (reported) when the value
// its commit names is not stored or is not what was committed. It does not
// list again, nor remember the record (Forget).
func (a *Anchored) ReadListed(ctx context.Context, m Member) (*Record, error) {
	h := m.History
	switch {
	case h == nil || h.Head == nil || h.Head.Gone():
		return nil, ErrNotFound
	case len(h.Forks) > 0:
		return nil, ErrForked
	}
	rec, err := a.committed(ctx, h.Head)
	if errors.Is(err, ErrLost) {
		a.mu.Lock()
		a.alarm(Regression{Name: h.Head.Name, Had: h.Head.Version, Found: h.Head.Version, Lost: true})
		a.mu.Unlock()
	}
	return rec, err
}

// Forget drops what this enclave remembers of name's commits (Seen), so a
// series of records made once and never read again does not grow its
// memory: a record forgotten is read again from its whole history, and the
// store of commits going back on it is then not noticed by this enclave.
func (a *Anchored) Forget(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.seen, name)
}
