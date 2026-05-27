package tools

import (
	"context"
	"strings"
	"testing"
)

func TestRunGo(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		code        string
		want        string
		wantErr     bool
		errContains string
	}{
		{
			name: "valid code with fmt",
			code: `package main
import "fmt"
func main() { fmt.Print("hello world") }
`,
			want:    "hello world",
			wantErr: false,
		},
		{
			name: "valid code with math",
			code: `package main
import (
	"fmt"
	"math"
)
func main() { fmt.Print(math.Sqrt(16)) }
`,
			want:    "4",
			wantErr: false,
		},
		{
			name: "invalid go code",
			code: `package main
func main() { this is not valid go code }
`,
			wantErr: true,
			errContains: "syntax error: unexpected name",
		},
		{
			name: "completely invalid go code",
			code: `this is completely invalid`,
			wantErr:     true,
			errContains: "invalid go code",
		},
		{
			name: "unallowed import os",
			code: `package main
import "os"
func main() { os.Exit(1) }
`,
			wantErr:     true,
			errContains: "import of \"os\" is not allowed for security reasons",
		},
		{
			name: "unallowed import os/exec",
			code: `package main
import "os/exec"
func main() { exec.Command("echo", "hacked").Run() }
`,
			wantErr:     true,
			errContains: "import of \"os/exec\" is not allowed",
		},
		{
			name: "unallowed import syscall",
			code: `package main
import "syscall"
func main() { syscall.Exit(1) }
`,
			wantErr:     true,
			errContains: "import of \"syscall\" is not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := runGo(ctx, tt.code)
			if (err != nil) != tt.wantErr {
				t.Errorf("runGo() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("runGo() error = %v, want err to contain %q", err, tt.errContains)
				}
				return
			}
			if got != tt.want {
				t.Errorf("runGo() = %q, want %q", got, tt.want)
			}
		})
	}
}
