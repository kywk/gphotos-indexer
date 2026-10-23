package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScaffold_Run(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "gphotos_scaffold_test_*")
	if err != nil {
		t.Fatalf("無法建立暫存目錄: %v", err)
	}
	defer os.RemoveAll(tempDir)

	targetDir := filepath.Join(tempDir, "kywk.id")

	// 測試首次執行 init
	cfg := Config{
		TargetDir: targetDir,
		Force:     false,
	}
	if err := Run(cfg); err != nil {
		t.Fatalf("Scaffold Run 失敗: %v", err)
	}

	// 檢查 data 目錄是否建立
	dataDir := filepath.Join(targetDir, "data")
	if info, err := os.Stat(dataDir); err != nil || !info.IsDir() {
		t.Fatalf("預期 data 目錄存在且為資料夾，實際: err=%v", err)
	}

	// 檢查 .gitignore
	gitignorePath := filepath.Join(targetDir, ".gitignore")
	gitignoreBytes, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("無法讀取 .gitignore: %v", err)
	}
	if !strings.Contains(string(gitignoreBytes), "data/") {
		t.Errorf("預期 .gitignore 包含 data/，實際: %s", string(gitignoreBytes))
	}

	// 檢查 Makefile
	makefilePath := filepath.Join(targetDir, "Makefile")
	makefileBytes, err := os.ReadFile(makefilePath)
	if err != nil {
		t.Fatalf("無法讀取 Makefile: %v", err)
	}
	if !strings.Contains(string(makefileBytes), "albums:") {
		t.Errorf("預期 Makefile 包含 albums 目標，實際: %s", string(makefileBytes))
	}

	// 檢查 README.md
	readmePath := filepath.Join(targetDir, "README.md")
	readmeBytes, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("無法讀取 README.md: %v", err)
	}
	if !strings.Contains(string(readmeBytes), "kywk.id") {
		t.Errorf("預期 README.md 包含專案名稱 kywk.id，實際: %s", string(readmeBytes))
	}

	// 測試重複執行不帶 Force：檔案內容不應被覆寫
	customContent := "# Custom Notes"
	if err := os.WriteFile(readmePath, []byte(customContent), 0644); err != nil {
		t.Fatalf("修改 README.md 失敗: %v", err)
	}

	if err := Run(cfg); err != nil {
		t.Fatalf("二次執行 Run 失敗: %v", err)
	}

	readBytesAfter, _ := os.ReadFile(readmePath)
	if string(readBytesAfter) != customContent {
		t.Errorf("不帶 Force 應跳過已存在檔案，實際被變更為: %s", string(readBytesAfter))
	}

	// 測試帶 Force：應覆寫回產生之模板
	cfg.Force = true
	if err := Run(cfg); err != nil {
		t.Fatalf("帶 Force 執行 Run 失敗: %v", err)
	}

	readBytesForce, _ := os.ReadFile(readmePath)
	if !strings.Contains(string(readBytesForce), "kywk.id") {
		t.Errorf("帶 Force 應覆寫為最新模板，實際: %s", string(readBytesForce))
	}
}
