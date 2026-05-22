package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

type Task struct {
	Title       string
	ID          string
	Total       int64
	Transferred int64
}

type MultiProgress struct {
	PreMessages   []string
	Tasks         []*Task
	PostMessages  []string
	linesRendered int
}

func NewMultiProgress(pre []string, post []string) *MultiProgress {
	return &MultiProgress{
		PreMessages:  pre,
		PostMessages: post,
	}
}

func (mp *MultiProgress) AddTask(title, id string, totalBytes int64) *Task {
	t := &Task{Title: title, Total: totalBytes, ID: id}
	mp.Tasks = append(mp.Tasks, t)
	return t
}

func (mp *MultiProgress) Render() {
	if mp.linesRendered > 0 {
		fmt.Printf("\033[%dA", mp.linesRendered)
	}

	var sb strings.Builder
	currentLines := 0

	for _, msg := range mp.PreMessages {
		sb.WriteString(msg + "\n")
		currentLines++
	}

	barWidth := 20
	allDone := true

	for _, task := range mp.Tasks {
		if task.Transferred < task.Total {
			allDone = false
		}

		var pct float64
		if task.Total > 0 {
			pct = (float64(task.Transferred) / float64(task.Total)) * 100
		}

		filled := int((pct / 100) * float64(barWidth))
		if filled > barWidth {
			filled = barWidth
		}
		empty := barWidth - filled

		barStr := strings.Repeat("=", filled) + strings.Repeat(" ", empty)
		formatedTransferred := FormatBytes(task.Transferred)
		formatedTotal := FormatBytes(task.Total)

		sb.WriteString(fmt.Sprintf("\033[K%s [%s] %3.0f%% (%s/%s)\n",
			task.Title, barStr, pct, formatedTransferred, formatedTotal))
		currentLines++
	}

	if allDone {
		for _, msg := range mp.PostMessages {
			sb.WriteString(msg + "\n")
			currentLines++
		}
	}

	fmt.Print(sb.String())
	os.Stdout.Sync()

	mp.linesRendered = currentLines
}

type ProgressProxy struct {
	Writer io.Writer
	Task   *Task
	Layout *MultiProgress
}

func (pp ProgressProxy) Write(p []byte) (int, error) {
	n, err := pp.Writer.Write(p)
	if n > 0 {
		pp.Task.Transferred += int64(n)
		pp.Layout.Render()
	}
	return n, err
}

func FormatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
