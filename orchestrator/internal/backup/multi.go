package backup

import (
	"fmt"
	"sort"
)

// MultiStore writes to one store and still lists, restores and prunes the
// archives earlier stores hold. Switching the target (local disk to S3) would
// otherwise hide every archive already taken: a re-created project would find
// no backup to restore its database from. The old archives stay reachable until
// retention drops them.
type MultiStore struct {
	primary Store
	older   []Store
}

// NewMultiStore writes to primary and also reads from older.
func NewMultiStore(primary Store, older ...Store) *MultiStore {
	return &MultiStore{primary: primary, older: older}
}

func (m *MultiStore) all() []Store { return append([]Store{m.primary}, m.older...) }

func (m *MultiStore) Create(workloadID, dataDir string, exclude []string) (Backup, error) {
	return m.primary.Create(workloadID, dataDir, exclude)
}

func (m *MultiStore) Put(workloadID, file, kind string) (Backup, error) {
	return m.primary.Put(workloadID, file, kind)
}

// List merges every store's backups, newest first. An id present in more than
// one store is listed once, from the first store that has it.
func (m *MultiStore) List(workloadID string) ([]Backup, error) {
	seen := map[string]bool{}
	var out []Backup
	for i, s := range m.all() {
		list, err := s.List(workloadID)
		if err != nil {
			if i == 0 {
				return nil, err
			}
			continue // an unreachable older store hides only its own archives
		}
		for _, b := range list {
			if !seen[b.ID] {
				seen[b.ID] = true
				out = append(out, b)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// owner is the store holding id.
func (m *MultiStore) owner(id string) (Store, error) {
	wid, _, _, err := parseID(id)
	if err != nil {
		return nil, err
	}
	for _, s := range m.all() {
		list, err := s.List(wid)
		if err != nil {
			continue
		}
		for _, b := range list {
			if b.ID == id {
				return s, nil
			}
		}
	}
	return nil, fmt.Errorf("backup %s not found", id)
}

func (m *MultiStore) Restore(id, destDir string) error {
	s, err := m.owner(id)
	if err != nil {
		return err
	}
	return s.Restore(id, destDir)
}

func (m *MultiStore) Fetch(id, destFile string) error {
	s, err := m.owner(id)
	if err != nil {
		return err
	}
	return s.Fetch(id, destFile)
}

func (m *MultiStore) Delete(id string) error {
	s, err := m.owner(id)
	if err != nil {
		return nil // already gone everywhere
	}
	return s.Delete(id)
}

func (m *MultiStore) Retain(workloadID string, keep int) error {
	list, err := m.List(workloadID)
	if err != nil {
		return err
	}
	for i := keep; i < len(list); i++ {
		_ = m.Delete(list[i].ID)
	}
	return nil
}
