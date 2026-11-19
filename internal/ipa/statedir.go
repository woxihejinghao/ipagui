package ipa

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/majd/ipatool/v2/pkg/util/operatingsystem"
)

// PrepareStateDirectory 是 cmd/state_directory.go 的逐字复刻。
//
// 上游是未导出函数（`prepareStateDirectory`），外部模块无法调用，只能复刻。
// 行为要点：
//   - 默认状态目录是 $HOME/.ipatool；若 $XDG_STATE_HOME 或 $XDG_DATA_HOME 是**绝对路径**，
//     则用其中的 ipatool 子目录（先 XDG_STATE_HOME，再 XDG_DATA_HOME）；
//   - 如果 legacy 目录已存在，会在必要时**迁移**（os.Rename）到 XDG 目录；
//     迁移失败时**保留 legacy 目录**（保住登录态优先）；
//   - 从不把目录移动进它自己；也从不在两边都存在时做合并。
func PrepareStateDirectory(os operatingsystem.OperatingSystem, homeDirectory string) (string, error) {
	legacyDirectory := filepath.Join(homeDirectory, ConfigDirectoryName)
	stateDirectory := legacyDirectory

	for _, variable := range []string{"XDG_STATE_HOME", "XDG_DATA_HOME"} {
		if base := os.Getenv(variable); filepath.IsAbs(base) {
			stateDirectory = filepath.Join(base, "ipatool")

			break
		}
	}

	info, err := os.Stat(legacyDirectory)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("could not read legacy state directory metadata: %w", err)
	}

	if err == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("legacy state path is not a directory: %s", legacyDirectory)
		}

		// Never merge sessions or move a directory into itself. Keeping the legacy
		// directory also preserves authentication when migration is unavailable.
		relative, relErr := filepath.Rel(legacyDirectory, stateDirectory)
		if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return legacyDirectory, nil
		}

		if _, statErr := os.Stat(stateDirectory); statErr == nil || !os.IsNotExist(statErr) {
			return legacyDirectory, nil
		}

		if mkErr := os.MkdirAll(filepath.Dir(stateDirectory), 0o700); mkErr != nil {
			return legacyDirectory, nil
		}

		// Rename keeps cookies and encrypted credentials together, with their
		// existing permissions. Cross-filesystem moves fall back to legacy storage.
		if renameErr := os.Rename(legacyDirectory, stateDirectory); renameErr != nil {
			return legacyDirectory, nil
		}

		return stateDirectory, nil
	}

	if mkErr := os.MkdirAll(stateDirectory, 0o700); mkErr != nil {
		return "", fmt.Errorf("failed to create state directory: %w", mkErr)
	}

	return stateDirectory, nil
}
