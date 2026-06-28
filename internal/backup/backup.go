package backup

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Start 立即执行一次备份，并启动每日定时备份的后台 goroutine。
func Start(dbPath string) {
	runBackup(dbPath)
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			runBackup(dbPath)
		}
	}()
}

// runBackup 复制数据库文件并清理旧备份，所有错误仅记录不致命。
func runBackup(dbPath string) {
	dst := dbPath + ".backup." + time.Now().Format("20060102")
	if err := copyFile(dbPath, dst); err != nil {
		log.Printf("[backup] 备份失败: %v", err)
		return
	}
	log.Printf("[backup] 数据库已备份到 %s", dst)
	pruneBackups(dbPath, 7)
}

// copyFile 用 io.Copy 复制文件内容。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// pruneBackups 仅保留最近 keep 个备份文件，删除其余较旧的。
func pruneBackups(dbPath string, keep int) {
	matches, err := filepath.Glob(dbPath + ".backup.*")
	if err != nil {
		log.Printf("[backup] 列出备份文件失败: %v", err)
		return
	}
	if len(matches) <= keep {
		return
	}
	sort.Strings(matches) // 文件名含日期，字典序即时间序
	for _, old := range matches[:len(matches)-keep] {
		if err := os.Remove(old); err != nil {
			log.Printf("[backup] 删除旧备份 %s 失败: %v", old, err)
		}
	}
}
