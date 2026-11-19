package services

import (
	"errors"
	"fmt"
	"os"

	"github.com/milan/ipagui/internal/ipa"
	"github.com/milan/ipagui/internal/store"
)

// LibraryService 是本地 IPA 清单的绑定层。
type LibraryService struct {
	runtime *Runtime
}

// NewLibraryService 构造本地库服务。
func NewLibraryService(runtimeInstance *Runtime) *LibraryService {
	return &LibraryService{runtime: runtimeInstance}
}

// List 列出本地清单，并标注文件是否仍在磁盘上。
//
// 为什么要带 Exists：用户在访达里删掉文件是很常见的操作，界面必须能如实显示
// 「记录还在、文件没了」，而不是等到点了「打开所在目录」才发现。
func (s *LibraryService) List(filter store.EntryFilter) ([]LibraryEntry, error) {
	if err := s.runtime.EnsureReady(); err != nil {
		return nil, err
	}

	entries, err := s.runtime.Library().ListEntries(s.runtime.CallContext(), filter)
	if err != nil {
		return nil, err
	}

	results := make([]LibraryEntry, 0, len(entries))
	for _, entry := range entries {
		item := LibraryEntry{Entry: entry}

		if _, statErr := os.Stat(entry.Path); statErr == nil {
			item.Exists = true
		} else if !errors.Is(statErr, os.ErrNotExist) {
			item.Exists = true // 权限等问题不要谎报「文件不存在」
			item.Note = statErr.Error()
		}

		results = append(results, item)
	}

	return results, nil
}

// LibraryEntry 是清单项 + 磁盘状态。
type LibraryEntry struct {
	store.Entry
	Exists bool   `json:"exists"`
	Note   string `json:"note,omitempty"`
}

// Delete 只删账本记录，不动磁盘上的文件。
func (s *LibraryService) Delete(id int64) error {
	if err := s.runtime.EnsureReady(); err != nil {
		return err
	}

	return s.runtime.Library().DeleteEntry(s.runtime.CallContext(), id)
}

// Reveal 在文件管理器里高亮这个 IPA。
func (s *LibraryService) Reveal(id int64) error {
	entry, err := s.entry(id)
	if err != nil {
		return err
	}

	if _, statErr := os.Stat(entry.Path); statErr != nil {
		return fmt.Errorf("文件已不在原位置：%s", entry.Path)
	}

	return revealInFileManager(entry.Path)
}

// Artwork 从磁盘上的 IPA 里取出图标，返回 data: URI 供界面直接显示。
//
// 用的是包里的 iTunesArtwork，不是再去问 Apple：公开接口只给当前商店版本的图标，
// 而这里的每一份是某个历史版本，包里的图标与包里的版本严格一致。
func (s *LibraryService) Artwork(id int64) (string, error) {
	if err := s.runtime.EnsureReady(); err != nil {
		return "", err
	}

	entry, err := s.entry(id)
	if err != nil {
		return "", err
	}

	if _, statErr := os.Stat(entry.Path); statErr != nil {
		return "", fmt.Errorf("文件已不在原位置：%s", entry.Path)
	}

	data, err := ipa.ReadIPArtwork(entry.Path)
	if err != nil {
		return "", err
	}

	return ipa.ArtworkDataURI(data), nil
}

// Hash 计算并保存这个 IPA 的 sha256（大文件，界面应放在后台并显示进度提示）。
func (s *LibraryService) Hash(id int64) (string, error) {
	if err := s.runtime.EnsureReady(); err != nil {
		return "", err
	}

	entry, err := s.entry(id)
	if err != nil {
		return "", err
	}

	digest, err := store.HashFile(s.runtime.CallContext(), entry.Path)
	if err != nil {
		return "", err
	}

	entry.SHA256 = digest
	if _, err := s.runtime.Library().UpsertEntry(s.runtime.CallContext(), entry); err != nil {
		return "", err
	}

	return digest, nil
}

func (s *LibraryService) entry(id int64) (store.Entry, error) {
	if err := s.runtime.EnsureReady(); err != nil {
		return store.Entry{}, err
	}

	return s.runtime.Library().GetEntry(s.runtime.CallContext(), id)
}
