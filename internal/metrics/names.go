package metrics

func init() {
	Describe("talaria_build_info", "gauge", "The running build; always 1.")
	Describe("talaria_uptime_seconds", "gauge", "Seconds since the gateway started.")
	Describe("talaria_messages_total", "counter", "Messages from users, by gateway and what became of them (accepted or rate_limited).")
	Describe("talaria_model_calls_total", "counter", "Requests to the model, by result (ok or error).")
	Describe("talaria_tool_calls_total", "counter", "Tool calls, by status (ok, error, or not_run for one refused or denied).")
	Describe("talaria_approvals_total", "counter", "Questions put to a person before a tool ran, by decision (allowed or denied).")
	Describe("talaria_jobs_total", "counter", "Scheduled jobs, goal check-ins, tasks and watch polls that ran, by kind and result.")
	Describe("talaria_watch_polls_total", "counter", "Watch polls, by what they came to (quiet, changed, failed, paused).")
}
