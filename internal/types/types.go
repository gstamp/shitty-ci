package types

type BuildState string

const (
	BuildPending   BuildState = "pending"
	BuildRunning   BuildState = "running"
	BuildSuccess   BuildState = "success"
	BuildFailure   BuildState = "failure"
	BuildTimedOut  BuildState = "timed_out"
	BuildCancelled BuildState = "cancelled"
)

type Repo struct {
	ID     int64  `json:"id"`
	Owner  string `json:"owner"`
	Name   string `json:"name"`
	Full   string `json:"full"`
}

type Build struct {
	ID          string     `json:"id"`
	Repo        string     `json:"repo"`
	SHA         string     `json:"sha"`
	Ref         string     `json:"ref"`
	State       BuildState `json:"state"`
	Step        string     `json:"step,omitempty"`
	CreatedAt   int64      `json:"created_at"`
	StartedAt   int64      `json:"started_at,omitempty"`
	FinishedAt  int64      `json:"finished_at,omitempty"`
	Description string     `json:"description,omitempty"`
}

type DaemonStatus struct {
	OK             bool   `json:"ok"`
	QueueDepth     int    `json:"queue_depth"`
	RunningBuilds  int    `json:"running_builds"`
	MaxConcurrent  int    `json:"max_concurrent"`
	PollInterval   string `json:"poll_interval"`
	DataDir        string `json:"data_dir"`
}
