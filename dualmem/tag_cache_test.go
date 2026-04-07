package dualmem

import "testing"

func TestTagCacheStoreLoad(t *testing.T) {
	store := newTestStore(t)

	tags := []Tag{
		{File: "foo.go", Name: "HandleReq", Kind: "def", SubKind: "function", Line: 42},
		{File: "foo.go", Name: "Request", Kind: "def", SubKind: "class", Line: 15},
		{File: "foo.go", Name: "HandleReq", Kind: "ref", SubKind: "function", Line: 88},
	}

	// Store tags
	err := store.StoreTags("ns1", "pkg/foo.go", "go", tags, 1700000000)
	if err != nil {
		t.Fatalf("StoreTags: %v", err)
	}

	// Load back
	loaded, mtime, err := store.LoadTags("ns1", "pkg/foo.go")
	if err != nil {
		t.Fatalf("LoadTags: %v", err)
	}
	if mtime != 1700000000 {
		t.Errorf("mtime = %d, want 1700000000", mtime)
	}
	if len(loaded) != 3 {
		t.Fatalf("got %d tags, want 3", len(loaded))
	}
	if loaded[0].Name != "HandleReq" || loaded[0].Kind != "def" || loaded[0].SubKind != "function" || loaded[0].Line != 42 {
		t.Errorf("tag[0] = %+v, want HandleReq def function @42", loaded[0])
	}
	if loaded[1].Name != "Request" || loaded[1].Kind != "def" || loaded[1].SubKind != "class" {
		t.Errorf("tag[1] = %+v, want Request def class", loaded[1])
	}
	if loaded[2].Name != "HandleReq" || loaded[2].Kind != "ref" {
		t.Errorf("tag[2] = %+v, want HandleReq ref", loaded[2])
	}
}

func TestTagCacheMiss(t *testing.T) {
	store := newTestStore(t)

	tags, mtime, err := store.LoadTags("ns1", "nonexistent.go")
	if err != nil {
		t.Fatalf("LoadTags miss: %v", err)
	}
	if tags != nil {
		t.Errorf("expected nil tags for cache miss, got %d tags", len(tags))
	}
	if mtime != 0 {
		t.Errorf("expected zero mtime for cache miss, got %d", mtime)
	}
}

func TestTagCacheMtimeInvalidation(t *testing.T) {
	store := newTestStore(t)

	tags := []Tag{
		{File: "bar.go", Name: "Foo", Kind: "def", SubKind: "function", Line: 1},
	}

	// Store with initial mtime
	if err := store.StoreTags("ns1", "bar.go", "go", tags, 1000); err != nil {
		t.Fatalf("StoreTags: %v", err)
	}

	// Update with new mtime (file changed)
	tags2 := []Tag{
		{File: "bar.go", Name: "Foo", Kind: "def", SubKind: "function", Line: 1},
		{File: "bar.go", Name: "Bar", Kind: "def", SubKind: "function", Line: 10},
	}
	if err := store.StoreTags("ns1", "bar.go", "go", tags2, 2000); err != nil {
		t.Fatalf("StoreTags update: %v", err)
	}

	// Verify updated data
	loaded, mtime, err := store.LoadTags("ns1", "bar.go")
	if err != nil {
		t.Fatalf("LoadTags: %v", err)
	}
	if mtime != 2000 {
		t.Errorf("mtime = %d, want 2000", mtime)
	}
	if len(loaded) != 2 {
		t.Errorf("got %d tags after update, want 2", len(loaded))
	}
}

func TestTagCacheDelete(t *testing.T) {
	store := newTestStore(t)

	tags := []Tag{
		{File: "baz.go", Name: "X", Kind: "def", SubKind: "function", Line: 1},
	}
	if err := store.StoreTags("ns1", "baz.go", "go", tags, 1000); err != nil {
		t.Fatalf("StoreTags: %v", err)
	}

	// Delete
	if err := store.DeleteTags("ns1", "baz.go"); err != nil {
		t.Fatalf("DeleteTags: %v", err)
	}

	// Verify gone
	loaded, mtime, err := store.LoadTags("ns1", "baz.go")
	if err != nil {
		t.Fatalf("LoadTags after delete: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil tags after delete, got %d tags", len(loaded))
	}
	if mtime != 0 {
		t.Errorf("expected zero mtime after delete, got %d", mtime)
	}
}

func TestTagCacheLoadAllMtimes(t *testing.T) {
	store := newTestStore(t)

	// Store tags for multiple files across two namespaces
	files := []struct {
		ns, path, lang string
		mtime          int64
		tags           []Tag
	}{
		{"ns1", "a.go", "go", 1000, []Tag{{File: "a.go", Name: "A", Kind: "def", SubKind: "function", Line: 1}}},
		{"ns1", "b.go", "go", 2000, []Tag{{File: "b.go", Name: "B", Kind: "def", SubKind: "function", Line: 1}}},
		{"ns1", "c.go", "go", 3000, []Tag{{File: "c.go", Name: "C", Kind: "def", SubKind: "function", Line: 1}}},
		{"ns2", "other.go", "go", 5000, []Tag{{File: "other.go", Name: "O", Kind: "def", SubKind: "function", Line: 1}}},
	}

	for _, f := range files {
		if err := store.StoreTags(f.ns, f.path, f.lang, f.tags, f.mtime); err != nil {
			t.Fatalf("StoreTags %s/%s: %v", f.ns, f.path, err)
		}
	}

	// Load mtimes for ns1
	mtimes, err := store.LoadAllTagMtimes("ns1")
	if err != nil {
		t.Fatalf("LoadAllTagMtimes: %v", err)
	}
	if len(mtimes) != 3 {
		t.Errorf("got %d entries for ns1, want 3", len(mtimes))
	}
	if mtimes["a.go"] != 1000 {
		t.Errorf("a.go mtime = %d, want 1000", mtimes["a.go"])
	}
	if mtimes["b.go"] != 2000 {
		t.Errorf("b.go mtime = %d, want 2000", mtimes["b.go"])
	}
	if mtimes["c.go"] != 3000 {
		t.Errorf("c.go mtime = %d, want 3000", mtimes["c.go"])
	}

	// ns2 should only have its own entry
	mtimes2, err := store.LoadAllTagMtimes("ns2")
	if err != nil {
		t.Fatalf("LoadAllTagMtimes ns2: %v", err)
	}
	if len(mtimes2) != 1 {
		t.Errorf("got %d entries for ns2, want 1", len(mtimes2))
	}
	if mtimes2["other.go"] != 5000 {
		t.Errorf("other.go mtime = %d, want 5000", mtimes2["other.go"])
	}
}

func TestTagCacheClear(t *testing.T) {
	store := newTestStore(t)

	tags := []Tag{{File: "x.go", Name: "X", Kind: "def", SubKind: "function", Line: 1}}
	store.StoreTags("ns1", "x.go", "go", tags, 1000)
	store.StoreTags("ns1", "y.go", "go", tags, 2000)
	store.StoreTags("ns2", "z.go", "go", tags, 3000)

	// Clear ns1 only
	if err := store.ClearTagCache("ns1"); err != nil {
		t.Fatalf("ClearTagCache: %v", err)
	}

	// ns1 should be empty
	mtimes, err := store.LoadAllTagMtimes("ns1")
	if err != nil {
		t.Fatalf("LoadAllTagMtimes after clear: %v", err)
	}
	if len(mtimes) != 0 {
		t.Errorf("ns1 has %d entries after clear, want 0", len(mtimes))
	}

	// ns2 should be untouched
	mtimes2, err := store.LoadAllTagMtimes("ns2")
	if err != nil {
		t.Fatalf("LoadAllTagMtimes ns2: %v", err)
	}
	if len(mtimes2) != 1 {
		t.Errorf("ns2 has %d entries, want 1", len(mtimes2))
	}
}

func TestTagCacheEmptyTags(t *testing.T) {
	store := newTestStore(t)

	// Store empty tags slice (file parsed but no symbols found)
	if err := store.StoreTags("ns1", "empty.go", "go", []Tag{}, 1000); err != nil {
		t.Fatalf("StoreTags empty: %v", err)
	}

	tags, mtime, err := store.LoadTags("ns1", "empty.go")
	if err != nil {
		t.Fatalf("LoadTags: %v", err)
	}
	if mtime != 1000 {
		t.Errorf("mtime = %d, want 1000", mtime)
	}
	if tags == nil {
		t.Error("tags should be non-nil empty slice, got nil")
	}
	if len(tags) != 0 {
		t.Errorf("got %d tags, want 0", len(tags))
	}
}
