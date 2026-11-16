package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	protocVersion       = "25.1"
	protobufGoVersion   = "v1.31.0"
	protocVersionOutput = "libprotoc " + protocVersion
	pluginVersionOutput = "protoc-gen-go " + protobufGoVersion
)

type toolPaths struct {
	protoc string
	plugin string
}

func installTools(tools toolPaths, goBin, source string, offline bool) error {
	if source != "" {
		abs, err := filepath.Abs(source)
		if err != nil {
			return err
		}
		if err := checkVersion(abs, protocVersionOutput); err != nil {
			return err
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(tools.protoc), 0755); err != nil {
			return err
		}
		if abs != tools.protoc {
			if err := os.WriteFile(tools.protoc, data, 0755); err != nil {
				return err
			}
		}
	}
	if err := checkVersion(tools.protoc, protocVersionOutput); err != nil {
		return fmt.Errorf("请先准备本平台的 protoc %s（见协议 README）：%w", protocVersion, err)
	}
	if offline {
		if err := os.Setenv("GOPROXY", "off"); err != nil {
			return err
		}
	}
	version, err := output(goBin, "list", "-mod=readonly", "-m", "-f", "{{.Version}}", "google.golang.org/protobuf")
	if err != nil {
		return err
	}
	if version != protobufGoVersion {
		return fmt.Errorf("生成器要求项目固定的 Protobuf %s，实际 %s", protobufGoVersion, version)
	}
	if checkVersion(tools.plugin, pluginVersionOutput) != nil {
		if _, err := output(goBin, "build", "-mod=readonly", "-o", tools.plugin, "google.golang.org/protobuf/cmd/protoc-gen-go"); err != nil {
			return err
		}
	}
	if err := checkVersion(tools.plugin, pluginVersionOutput); err != nil {
		return err
	}
	fmt.Printf("工具已就绪：protoc %s / %s\n", protocVersion, pluginVersionOutput)
	return nil
}

func output(program string, args ...string) (string, error) {
	data, err := exec.Command(program, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("执行 %s 失败：%w\n%s", program, err, data)
	}
	return strings.TrimSpace(string(data)), nil
}

func checkVersion(program, want string) error {
	got, err := output(program, "--version")
	if err != nil {
		return err
	}
	got = strings.Replace(got, "protoc-gen-go.exe", "protoc-gen-go", 1)
	if got != want {
		return fmt.Errorf("%s 版本不符：需要 %s，实际 %s", program, want, got)
	}
	return nil
}
