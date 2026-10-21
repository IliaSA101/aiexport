package bundle

import (
	"path"
	"strings"
)

// langFor подбирает язык для фенса по имени файла. Неизвестное расширение даёт
// пустую строку — фенс без языка, это безопасно.
func langFor(rel string) string {
	base := strings.ToLower(path.Base(rel))
	switch base {
	case "dockerfile":
		return "dockerfile"
	case "makefile":
		return "makefile"
	case "go.mod", "go.sum":
		return "go"
	}
	switch strings.TrimPrefix(path.Ext(base), ".") {
	case "go":
		return "go"
	case "php":
		return "php"
	case "js", "mjs", "cjs":
		return "javascript"
	case "ts":
		return "typescript"
	case "tsx":
		return "tsx"
	case "jsx":
		return "jsx"
	case "py":
		return "python"
	case "java":
		return "java"
	case "kt", "kts":
		return "kotlin"
	case "cs":
		return "csharp"
	case "rs":
		return "rust"
	case "rb":
		return "ruby"
	case "c", "h":
		return "c"
	case "cpp", "cc", "hpp":
		return "cpp"
	case "sh", "bash":
		return "bash"
	case "ps1":
		return "powershell"
	case "sql":
		return "sql"
	case "json":
		return "json"
	case "yaml", "yml":
		return "yaml"
	case "toml":
		return "toml"
	case "xml":
		return "xml"
	case "html", "htm":
		return "html"
	case "css":
		return "css"
	case "scss":
		return "scss"
	case "md", "markdown":
		return "markdown"
	case "proto":
		return "protobuf"
	case "puml", "plantuml":
		return "plantuml"
	case "tf":
		return "hcl"
	}
	return ""
}
