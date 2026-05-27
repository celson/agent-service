package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type skillMeta struct {
	uses []string
	body string
}

// LoadSkill carrega uma skill e todas as suas dependências (declaradas em `uses:`),
// resolvendo-as recursivamente e retornando o conteúdo concatenado na ordem correta
// (dependências primeiro, skill principal por último).
func LoadSkill(dir, name string) (string, error) {
	visited := map[string]bool{}
	var parts []string
	if err := loadSkillRec(dir, name, visited, &parts); err != nil {
		return "", err
	}
	return strings.Join(parts, "\n\n---\n\n"), nil
}

func loadSkillRec(dir, name string, visited map[string]bool, parts *[]string) error {
	if visited[name] {
		return nil
	}
	visited[name] = true

	meta, err := parseSkillFile(filepath.Join(dir, name+".md"))
	if err != nil {
		return fmt.Errorf("skill %q: %w", name, err)
	}

	for _, dep := range meta.uses {
		if err := loadSkillRec(dir, dep, visited, parts); err != nil {
			return err
		}
	}

	*parts = append(*parts, meta.body)
	return nil
}

func parseSkillFile(path string) (*skillMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	content := string(data)
	meta := &skillMeta{}

	// Frontmatter delimitado por ---
	if strings.HasPrefix(content, "---") {
		end := strings.Index(content[3:], "---")
		if end >= 0 {
			front := content[3 : end+3]
			meta.uses = parseFrontmatterUses(front)
			content = strings.TrimSpace(content[end+6:])
		}
	}

	meta.body = content
	return meta, nil
}

// parseFrontmatterUses extrai o valor de `uses:` do frontmatter.
// Aceita tanto `uses: skill1, skill2` quanto lista YAML com `- skill`.
func parseFrontmatterUses(front string) []string {
	var uses []string
	inUsesList := false

	for _, line := range strings.Split(front, "\n") {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "uses:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "uses:"))
			if val != "" && val != "~" {
				for {
					cidx := strings.IndexByte(val, ',')
					if cidx == -1 {
						if name := strings.TrimSpace(val); name != "" {
							uses = append(uses, name)
						}
						break
					}
					if name := strings.TrimSpace(val[:cidx]); name != "" {
						uses = append(uses, name)
					}
					val = val[cidx+1:]
				}
				inUsesList = false
			} else {
				inUsesList = true
			}
			continue
		}

		if inUsesList && strings.HasPrefix(line, "- ") {
			uses = append(uses, strings.TrimPrefix(line, "- "))
			continue
		}

		if inUsesList && line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "-") {
			inUsesList = false
		}
	}

	return uses
}
