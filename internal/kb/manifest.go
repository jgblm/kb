package kb

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/stone/kb/internal/model"
)

// ManifestVersion 当前索引格式版本。
const ManifestVersion = 1

// Manifest 是 .kb-manifest.yaml 的内存表示。
//
// manifest 是**可重建的派生索引**：真正的源是各 .md 文件的 frontmatter。
// 任何时候都能用 `kb reindex` 从 md 重建，所以它坏掉不致命。
type Manifest struct {
	Version int
	Entries []*model.Entry

	index map[string]*model.Entry
}

type manifestDoc struct {
	Version     int            `yaml:"version"`
	GeneratedAt string         `yaml:"generated_at"`
	Count       int            `yaml:"count"`
	Entries     []*model.Entry `yaml:"entries"`
}

// NewManifest 空索引。
func NewManifest() *Manifest {
	return &Manifest{Version: ManifestVersion, index: map[string]*model.Entry{}}
}

// Reindex 重建 id 索引。
func (m *Manifest) Reindex() {
	m.index = make(map[string]*model.Entry, len(m.Entries))
	for _, entry := range m.Entries {
		m.index[entry.ID] = entry
	}
}

// Len 条目数。
func (m *Manifest) Len() int { return len(m.Entries) }

// Get 按 id 取条目。
func (m *Manifest) Get(id string) *model.Entry {
	if m.index == nil {
		m.Reindex()
	}
	return m.index[id]
}

// Has 判断 id 是否存在。
func (m *Manifest) Has(id string) bool { return m.Get(id) != nil }

// Upsert 新增或替换条目。
func (m *Manifest) Upsert(entry *model.Entry) {
	if m.index == nil {
		m.Reindex()
	}
	if existing, ok := m.index[entry.ID]; ok {
		for index, item := range m.Entries {
			if item == existing {
				m.Entries[index] = entry
				break
			}
		}
	} else {
		m.Entries = append(m.Entries, entry)
	}
	m.index[entry.ID] = entry
}

// Remove 删除条目。
func (m *Manifest) Remove(id string) bool {
	if m.index == nil {
		m.Reindex()
	}
	if _, ok := m.index[id]; !ok {
		return false
	}
	delete(m.index, id)
	for index, item := range m.Entries {
		if item.ID == id {
			m.Entries = append(m.Entries[:index], m.Entries[index+1:]...)
			break
		}
	}
	return true
}

// All 全部条目（原顺序）。
func (m *Manifest) All() []*model.Entry { return m.Entries }

// SortedByUpdated 按 updated_at 排序。
func (m *Manifest) SortedByUpdated(desc bool) []*model.Entry {
	out := make([]*model.Entry, len(m.Entries))
	copy(out, m.Entries)
	sort.SliceStable(out, func(i, j int) bool {
		if desc {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].UpdatedAt < out[j].UpdatedAt
	})
	return out
}

// ByDocPath 按文档路径查。
func (m *Manifest) ByDocPath(docPath string) []*model.Entry {
	var out []*model.Entry
	for _, entry := range m.Entries {
		if entry.DocPath == docPath {
			out = append(out, entry)
		}
	}
	return out
}

// ByRepo 按仓库查。
func (m *Manifest) ByRepo(repo string) []*model.Entry {
	var out []*model.Entry
	for _, entry := range m.Entries {
		if entry.Anchor != nil && entry.Anchor.Repo == repo {
			out = append(out, entry)
		}
	}
	return out
}

// ByCodePath 给定代码路径（相对仓库根）查所有锚定其上的条目。
func (m *Manifest) ByCodePath(repo, codePath string) []*model.Entry {
	normalized := normalizeCodePath(codePath)
	var out []*model.Entry
	for _, entry := range m.Entries {
		if entry.Anchor != nil && entry.Anchor.Repo == repo && normalizeCodePath(entry.Anchor.Path) == normalized {
			out = append(out, entry)
		}
	}
	return out
}

// Stale 过期条目。
func (m *Manifest) Stale() []*model.Entry {
	var out []*model.Entry
	for _, entry := range m.Entries {
		if entry.Status == model.StatusStale {
			out = append(out, entry)
		}
	}
	return out
}

// Fresh 新鲜条目。
func (m *Manifest) Fresh() []*model.Entry {
	var out []*model.Entry
	for _, entry := range m.Entries {
		if entry.Status == model.StatusFresh {
			out = append(out, entry)
		}
	}
	return out
}

// LoadManifest 读索引。
//
// 文件缺失或内容损坏都返回空索引——md 文件才是唯一事实来源，
// 索引坏掉不应该连带知识库都打不开。损坏时第二个返回值是可展示的告警文本，
// 调用方应提示用户执行 `kb reindex`。error 只在真正的 IO 故障时非 nil。
func LoadManifest(path string) (*Manifest, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewManifest(), "", nil
		}
		return NewManifest(), "", err
	}
	if len(raw) == 0 {
		return NewManifest(), "", nil
	}
	doc := manifestDoc{}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return NewManifest(),
			fmt.Sprintf("索引文件 %s 解析失败（%v），已按空索引启动；执行 kb reindex 可从 md 重建", path, err),
			nil
	}
	manifest := NewManifest()
	manifest.Version = doc.Version
	if manifest.Version == 0 {
		manifest.Version = ManifestVersion
	}
	for _, entry := range doc.Entries {
		if entry == nil || entry.ID == "" {
			continue
		}
		manifest.Entries = append(manifest.Entries, entry)
	}
	manifest.Reindex()
	return manifest, "", nil
}

// Save 原子写索引：先写同目录临时文件再 rename，避免半截文件。
func (m *Manifest) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	doc := manifestDoc{
		Version:     ManifestVersion,
		GeneratedAt: model.NowISO(),
		Count:       len(m.Entries),
		Entries:     m.SortedByUpdated(false),
	}
	payload, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".manifest-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	if _, err := temp.Write(payload); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}
