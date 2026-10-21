package bundle

import (
	"slices"
	"strings"

	"github.com/IliaSA101/aiexport/internal/model"
)

// node — узел дерева для печати. Дерево строится из плоского списка путей:
// в Plan каталоги отдельными записями не хранятся.
type node struct {
	name  string
	note  string // пометка после имени, например «не включён: binary»
	isDir bool
	kids  []*node
	index map[string]*node
}

func newNode(name string, isDir bool) *node {
	return &node{name: name, isDir: isDir, index: map[string]*node{}}
}

// inTree решает, показывать ли запись в дереве. Исключённое правилами уезжает
// в раздел «Что сознательно не включено», иначе дерево утонуло бы в мусоре.
func inTree(e model.FileEntry) bool {
	switch e.Reason {
	case model.ReasonGitignore, model.ReasonDefault, model.ReasonGlob:
		return false
	}
	return true
}

func (n *node) insert(e model.FileEntry, treeMode bool) {
	parts := strings.Split(e.Rel, "/")
	cur := n
	for i, part := range parts {
		last := i == len(parts)-1
		child, ok := cur.index[part]
		if !ok {
			child = newNode(part, !last || e.Dir)
			cur.index[part] = child
			cur.kids = append(cur.kids, child)
		}
		cur = child
	}
	// Пометка нужна там, где файл есть, а содержимого нет по причине, о которой
	// читателю надо знать. В режиме tree пустое содержимое — норма и сказано выше.
	if e.Status == model.StatusSkipped && !treeMode {
		cur.note = "не включён: " + string(e.Reason)
	}
}

func (n *node) sort() {
	slices.SortFunc(n.kids, func(a, b *node) int {
		if a.isDir != b.isDir {
			if a.isDir {
				return -1
			}
			return 1
		}
		return strings.Compare(a.name, b.name)
	})
	for _, k := range n.kids {
		k.sort()
	}
}

// print печатает потомков с псевдографикой.
func (n *node) print(o *out, prefix string) {
	for i, k := range n.kids {
		branch, next := "├── ", "│   "
		if i == len(n.kids)-1 {
			branch, next = "└── ", "    "
		}
		name := clean(k.name)
		if k.isDir {
			name += "/"
		}
		if k.note != "" {
			name += "  [" + k.note + "]"
		}
		o.p("%s%s%s\n", prefix, branch, name)
		k.print(o, prefix+next)
	}
}

// clean заменяет управляющие символы: имена файлов приходят из чужой ФС, и
// перевод строки в имени сломал бы разметку бандла.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}
