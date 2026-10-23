package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	TargetDir string
	Force     bool
}

func Run(cfg Config) error {
	targetDir := cfg.TargetDir
	if targetDir == "" {
		targetDir = "."
	}

	absDir, err := filepath.Abs(targetDir)
	if err != nil {
		return fmt.Errorf("解析目錄路徑失敗: %w", err)
	}

	projectName := filepath.Base(absDir)
	if projectName == "." || projectName == "/" || projectName == "" {
		projectName = "Google Photos"
	}

	// 建立專案目錄與 data 子目錄
	dataDir := filepath.Join(absDir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("建立目錄失敗 %s: %w", dataDir, err)
	}

	createdCount := 0

	// 1. .gitignore
	gitignorePath := filepath.Join(absDir, ".gitignore")
	gitignoreContent := "# Google Photos Indexer Data Directory\ndata/\n"
	if written, err := writeFileIfNotExist(gitignorePath, gitignoreContent, cfg.Force); err != nil {
		return err
	} else if written {
		fmt.Printf("  ✔ 已建立: %s\n", filepath.Join(targetDir, ".gitignore"))
		createdCount++
	}

	// 2. Makefile
	makefilePath := filepath.Join(absDir, "Makefile")
	makefileContent := generateMakefile()
	if written, err := writeFileIfNotExist(makefilePath, makefileContent, cfg.Force); err != nil {
		return err
	} else if written {
		fmt.Printf("  ✔ 已建立: %s\n", filepath.Join(targetDir, "Makefile"))
		createdCount++
	}

	// 3. README.md
	readmePath := filepath.Join(absDir, "README.md")
	readmeContent := generateReadme(projectName)
	if written, err := writeFileIfNotExist(readmePath, readmeContent, cfg.Force); err != nil {
		return err
	} else if written {
		fmt.Printf("  ✔ 已建立: %s\n", filepath.Join(targetDir, "README.md"))
		createdCount++
	}

	fmt.Printf("\n✨ 成功初始化工作目錄: %s (建立 %d 個檔案，資料目錄: %s)\n", targetDir, createdCount, filepath.Join(targetDir, "data"))
	fmt.Println("\n後續操作指引:")
	if targetDir != "." {
		fmt.Printf("  cd %s\n", targetDir)
	}
	fmt.Println("  1. 授權登入: make auth")
	fmt.Println("  2. 索引相簿: make albums")
	fmt.Println("  3. 啟動搜尋: make serve")

	return nil
}

func writeFileIfNotExist(path string, content string, force bool) (bool, error) {
	if !force {
		if _, err := os.Stat(path); err == nil {
			fmt.Printf("  ↷ 跳過已存在檔案: %s (使用 -force 覆寫)\n", path)
			return false, nil
		}
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0644); err != nil {
		return false, fmt.Errorf("寫入檔案失敗 %s: %w", path, err)
	}
	return true, nil
}

func generateMakefile() string {
	return `GPHOTOS_INDEXER ?= $(shell which gphotos-indexer 2>/dev/null || echo $(HOME)/.files/bin/gphotos-indexer)
DATA_DIR ?= ./data
PORT ?= 8080

.PHONY: all auth albums albums-headful sync serve clean

all: albums

# 1. 首次 Google 帳號授權登入 (彈出 Chrome 視窗，手動登入後於終端按 Enter)
auth:
	$(GPHOTOS_INDEXER) auth -data $(DATA_DIR)

# 2. 抓取所有個人相簿清單與相簿內相片關聯 (背景無頭模式)
albums:
	$(GPHOTOS_INDEXER) albums -data $(DATA_DIR)

# 顯示 Chrome 視窗抓取相簿 (方便觀看進度或偵錯)
albums-headful:
	$(GPHOTOS_INDEXER) albums -data $(DATA_DIR) -headless=false

# 3. 抓取主時間軸相片 (可選)
sync:
	$(GPHOTOS_INDEXER) sync -data $(DATA_DIR) -max-items 200

# 4. 啟動本地搜尋 Web UI 檢視已抓取的相簿與相片
serve:
	$(GPHOTOS_INDEXER) serve -data $(DATA_DIR) -port $(PORT)

clean:
	@echo "注意：這將會清除 ./data 下的縮圖與資料庫，若確認請手動執行 rm -rf ./data"
`
}

func generateReadme(projectName string) string {
	return fmt.Sprintf(`# %s Google Photos 相簿抓取環境

本環境已透過 `+"`gphotos-indexer init`"+` 初始化，專門用於抓取與索引 %s 的 Google Photos 相簿與相片資料。

## 執行檔路徑
- 預設呼叫系統 PATH 中的 `+"`gphotos-indexer`"+` 或 `+"`~/.files/bin/gphotos-indexer`"+`

## 快速使用流程

### 步驟 1：Google 帳號授權登入
首次使用需建立授權 Session（Chrome 專屬 Profile 會儲存於 `+"`./data/chrome-profile`"+`）：
`+"```bash"+`
make auth
`+"```"+`
> 執行後會彈出 Chrome 視窗導向 Google Photos，請登入您的 Google 帳號。確認看到相片後，回到終端機按下 `+"`Enter`"+` 保存 Session。

### 步驟 2：開始抓取所有個人相簿資料
`+"```bash"+`
make albums
`+"```"+`
若希望即時看到瀏覽器捲動進度，可執行有頭模式：
`+"```bash"+`
make albums-headful
`+"```"+`

### 步驟 3：抓取主時間軸相片（選配）
`+"```bash"+`
make sync
`+"```"+`

### 步驟 4：啟動本地 Web 介面瀏覽與檢索
`+"```bash"+`
make serve
`+"```"+`
開啟瀏覽器至 `+"`http://localhost:8080`"+` 即可依相簿名稱、日期、類型進行極速搜尋與檢視相片。
`, projectName, projectName)
}
