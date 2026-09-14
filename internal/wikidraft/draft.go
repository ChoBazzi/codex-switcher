// Package wikidraft captures a Codex-authored Wiki without executing file tools.
package wikidraft

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ChoBazzi/codex-switcher/internal/checkpoint"
)

var ErrPublish = errors.New("checkpoint_publish_failed")

type Draft struct {
	bytes.Buffer
	request   *checkpoint.Request
	directory string
	home      string
	failed    bool
}

// Staging stays inside the caller's private, locked CLI home.
func New(home string, origin checkpoint.Origin) (*Draft, error) {
	dir, err := os.MkdirTemp(home, "wiki-draft-")
	if err != nil {
		return nil, ErrPublish
	}
	r, err := checkpoint.New(dir, origin)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	d := &Draft{request: r, directory: dir, home: home}
	// Supersede prior registrations before a model can receive this request.
	if err := d.Retain(true); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func (d *Draft) Close()          { d.request.Close(); os.RemoveAll(d.directory) }
func (d *Draft) MarkSent() error { return d.request.MarkSent() }
func (d *Draft) Write(p []byte) (int, error) {
	if d.failed || d.Len()+len(p) > checkpoint.MaxBytes {
		d.failed = true
		return 0, checkpoint.ErrSize
	}
	return d.Buffer.Write(p)
}

func (d *Draft) Prompt() string {
	return "현재 대화의 작업 상태를 다음 세션에 인계할 Wiki로 작성하세요. 도구를 사용하거나 작업을 더 진행하지 마세요. " +
		"아래 형식을 유지하고 각 섹션의 자리표시자를 실제 요약으로 바꾸세요. 없는 항목은 없음으로, 미확인 결과는 미확인으로 명시하세요. " +
		"전체 대화 원문, 인증정보, 계정 식별자, 서버 continuation ID를 넣지 마세요. " +
		"Changes에는 변경 파일과 검증 결과, TODO에는 다음 작업과 불확실성을 기록하세요. " +
		"코드 펜스나 서문 없이 문서만 답하세요. 메타데이터와 완료 마커는 그대로 유지하세요.\n\n" +
		string(checkpoint.Format(d.request.Metadata(), "목표 요약", "변경 및 검증 요약", "결정 요약", "다음 작업 및 미확인 사항"))
}

// Publish validates the exact bytes before replacing the project's latest file.
// This is storage completion, not safe-boundary or target-account approval.
func (d *Draft) Publish(projectDir string) (checkpoint.Snapshot, error) {
	if d.failed {
		return checkpoint.Snapshot{}, checkpoint.ErrSize
	}
	if err := writeAtomic(d.directory, "checkpoint.md", d.Bytes()); err != nil {
		return checkpoint.Snapshot{}, err
	}
	snap, err := d.request.Validate()
	if err != nil {
		return checkpoint.Snapshot{}, err
	}
	if err := publishSnapshot(projectDir, snap); err != nil {
		return checkpoint.Snapshot{}, err
	}
	return snap, nil
}

func publishSnapshot(project string, snap checkpoint.Snapshot) error {
	o := snap.Metadata.Origin
	dir, err := isolatedDirectory(project, []string{".codex-switcher", o.Worktree, o.Branch, o.Session})
	if err != nil {
		return err
	}
	return writeAtomic(dir, "checkpoint.md", []byte(snap.Markdown))
}
func (d *Draft) ID() string          { return d.request.Metadata().ID }
func (d *Draft) Deadline() time.Time { return d.request.Status().Deadline }

func writeAtomic(dir, name string, b []byte) error {
	temp := filepath.Join(dir, "checkpoint-"+rand.Text()+".tmp")
	f, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ErrPublish
	}
	defer os.Remove(temp)
	n, err := f.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	syncErr, closeErr := f.Sync(), f.Close()
	if err != nil || syncErr != nil || closeErr != nil {
		return ErrPublish
	}
	if os.Rename(temp, filepath.Join(dir, name)) != nil {
		return ErrPublish
	}
	directory, err := os.Open(dir)
	if err != nil {
		return ErrPublish
	}
	defer directory.Close()
	if directory.Sync() != nil {
		return ErrPublish
	}
	return nil
}
