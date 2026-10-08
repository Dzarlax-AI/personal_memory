package aipolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Budget stores conservative reservations before any provider dispatch.
// A server owns one instance; CLI evaluation uses a separate state directory.
type Budget struct {
	mu        sync.Mutex
	dir       string
	limits    Limits
	state     budgetState
	semaphore chan struct{}
}
type budgetState struct {
	Day     string           `json:"day"`
	Calls   int64            `json:"calls"`
	Tokens  int64            `json:"tokens"`
	Next    uint64           `json:"next"`
	Pending map[string]int64 `json:"pending"`
}
type Reservation struct {
	budget *Budget
	ID     string
	once   sync.Once
}

func NewBudget(dir string, limits Limits) (*Budget, error) {
	if limits.DailyCalls < 1 || limits.DailyInputTokens < 1 || limits.ReservationTokens < 1 || limits.ReservationTokens > limits.DailyInputTokens {
		return nil, errors.New("invalid inference budget limits")
	}
	if err := SecureDir(dir); err != nil {
		return nil, err
	}
	b := &Budget{dir: dir, limits: limits, semaphore: make(chan struct{}, 1)}
	raw, err := ReadPrivate(filepath.Join(dir, "budget.json"), 1<<20)
	if err == nil {
		if json.Unmarshal(raw, &b.state) != nil || !validLedger(b.state) {
			return nil, errors.New("invalid inference budget ledger")
		}
	} else if _, err2 := os.Lstat(filepath.Join(dir, "budget.json")); !os.IsNotExist(err2) {
		return nil, errors.New("cannot load inference budget ledger")
	}
	if b.state.Pending == nil {
		b.state.Pending = map[string]int64{}
	}
	return b, nil
}

func validLedger(s budgetState) bool {
	day, err := time.Parse("2006-01-02", s.Day)
	if err != nil || day.Format("2006-01-02") != s.Day || s.Calls < 0 || s.Tokens < 0 || s.Pending == nil || uint64(s.Calls) > s.Next || int64(len(s.Pending)) > s.Calls {
		return false
	}
	var total int64
	for id, value := range s.Pending {
		if !strings.HasPrefix(id, s.Day+"-") || value <= 0 || value > s.Tokens-total {
			return false
		}
		total += value
	}
	return true
}

// Begin never waits for queue capacity. Full inference queues fail open.
func (b *Budget) Begin(now time.Time) (*Reservation, error) {
	select {
	case b.semaphore <- struct{}{}:
	default:
		return nil, errors.New("inference queue full")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	fail := func(err error) (*Reservation, error) { <-b.semaphore; return nil, err }
	day := now.UTC().Format("2006-01-02")
	if b.state.Day > day {
		return fail(errors.New("inference clock moved backwards"))
	}
	if b.state.Day != day {
		b.state = budgetState{Day: day, Pending: map[string]int64{}}
	}
	if b.state.Calls >= b.limits.DailyCalls || b.state.Tokens > b.limits.DailyInputTokens-b.limits.ReservationTokens {
		return fail(errors.New("inference budget exhausted"))
	}
	b.state.Next++
	id := fmt.Sprintf("%s-%d", day, b.state.Next)
	b.state.Calls++
	b.state.Tokens += b.limits.ReservationTokens
	b.state.Pending[id] = b.limits.ReservationTokens
	if err := b.save(); err != nil {
		return fail(err)
	}
	return &Reservation{budget: b, ID: id}, nil
}

// Finish retains unknown reservations, including calls canceled after dispatch.
func (r *Reservation) Finish(inputTokens int64, known bool) {
	r.once.Do(func() {
		b := r.budget
		b.mu.Lock()
		defer b.mu.Unlock()
		reserved, ok := b.state.Pending[r.ID]
		if ok && known && inputTokens >= 0 {
			oldTokens := b.state.Tokens
			if inputTokens > reserved {
				// Usage beyond the serialized-payload bound is not trustworthy.
				// Exhaust this day's allowance without risking arithmetic overflow.
				b.state.Tokens = b.limits.DailyInputTokens
			} else {
				b.state.Tokens -= reserved - inputTokens
			}
			delete(b.state.Pending, r.ID)
			if err := b.save(); err != nil {
				b.state.Tokens = oldTokens
				b.state.Pending[r.ID] = reserved
			}
		}
		<-b.semaphore
	})
}
func (b *Budget) save() error {
	raw, err := json.Marshal(b.state)
	if err != nil {
		return err
	}
	return AtomicWrite(b.dir, "budget.json", raw)
}

// SecureDir rejects symlinks on every directory component, including ancestors.
func SecureDir(dir string) error {
	full, err := filepath.Abs(dir)
	if err != nil {
		return errors.New("invalid private state directory")
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(full, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if mkdirErr := os.Mkdir(current, 0700); mkdirErr != nil && !os.IsExist(mkdirErr) {
				return errors.New("cannot create private state directory")
			}
			// Another process may have created this component. Validate it
			// before continuing; EEXIST does not establish a safe directory.
			info, err = os.Lstat(current)
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe private state directory")
		}
	}
	if err := os.Chmod(full, 0700); err != nil {
		return errors.New("cannot protect private state directory")
	}
	return nil
}

// AtomicWrite replaces a private local state file without following a target symlink.
func AtomicWrite(dir, name string, raw []byte) error {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return errors.New("invalid state filename")
	}
	if err := SecureDir(dir); err != nil {
		return err
	}
	target := filepath.Join(dir, name)
	if i, err := os.Lstat(target); err == nil && (i.Mode()&os.ModeSymlink != 0 || !i.Mode().IsRegular()) {
		return errors.New("unsafe state target")
	}
	f, err := os.CreateTemp(dir, ".state-")
	if err != nil {
		return errors.New("cannot create private state file")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if f.Chmod(0600) != nil {
		return errors.New("cannot protect private state file")
	}
	if _, err = f.Write(raw); err != nil {
		return errors.New("cannot write private state file")
	}
	if f.Sync() != nil || f.Close() != nil {
		return errors.New("cannot sync private state file")
	}
	if os.Rename(f.Name(), target) != nil {
		return errors.New("cannot publish private state file")
	}
	d, err := os.Open(dir)
	if err != nil {
		return errors.New("cannot sync state directory")
	}
	defer d.Close()
	return d.Sync()
}
