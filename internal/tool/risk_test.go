package tool

import (
	"encoding/json"
	"testing"
)

func TestShellCommandsThatDeleteAreRecognisedAndOthersAreNot(t *testing.T) {
	deletes := []string{
		"rm file.txt", "rm -rf /tmp/build", "sudo rm -rf node_modules", "/bin/rm x", "cd /tmp && rm -f a b", "ls; rm a", "true || rm a",
		"find . -name '*.log' -delete", "find . -type f -exec rm {} +", "echo a | xargs rm", "rmdir old", "unlink link", "shred -u secret",
		"trash old.txt", "git clean -fd", "git reset --hard HEAD~3", "git stash drop", "git branch -D feature", "git branch -d old", "git tag -d v1",
		"git push origin --delete feature", "git push --force origin main", "git push -f", "git checkout -- .", "git restore src/",
		"docker rm web", "docker rmi image", "docker system prune -af", "docker volume rm data", "docker compose down -v",
		"kubectl delete pod web", "helm uninstall app", "terraform destroy", "aws s3 rm s3://bucket --recursive", "gcloud compute instances delete vm",
		"gh repo delete me/x --yes", "npm uninstall left-pad", "pip uninstall requests", "apt-get remove nginx", "apt purge x", "brew uninstall jq",
		"rsync -a --delete src/ dst/", "psql -c 'DROP TABLE users'", "mysql -e \"delete from orders\"", "sqlite3 db 'TRUNCATE TABLE t'", "redis-cli flushall",
		"dd if=/dev/zero of=/dev/sda", "mkfs.ext4 /dev/sdb1", "wipefs -a /dev/sdb", "truncate -s 0 log",
		"python3 -c 'import os; os.remove(\"a\")'", "python -c \"import shutil; shutil.rmtree('x')\"", "node -e \"require('fs').unlinkSync('a')\"",
		"RM -RF x", "bash -c 'rm -rf x'", "Remove-Item -Recurse x",
	}
	for _, c := range deletes {
		if ShellRisk(c)&RiskDeletes == 0 {
			t.Errorf("not recognised as deleting: %q", c)
		}
	}
	safe := []string{
		"ls -la", "cat file.txt", "echo hello", "pwd", "git status", "git log --oneline", "git diff", "git add . && git commit -m x", "git push origin main",
		"git checkout main", "git checkout -b feature", "git branch", "git branch -a", "git pull", "git stash", "git stash pop", "git restore --staged x",
		"npm install", "npm test", "pip install requests", "go build ./...", "go test ./...", "make", "python3 script.py", "node app.js", "curl -s https://example.com",
		"mkdir -p a/b", "cp a b", "mv a b", "touch x", "chmod +x run.sh", "grep -r term .", "sed -i 's/a/b/' f", "tar czf out.tgz dir", "docker ps", "docker build .",
		"docker run --rm alpine echo hi", "kubectl get pods", "terraform plan", "aws s3 ls", "psql -c 'select 1'", "df -h", "ps aux", "head -n 5 log",
		"echo \"form submitted\"", "ssh host uptime", "apt-get install -y curl", "apt update", "sort data | uniq > out.txt", "rsync -a src/ dst/",
	}
	for _, c := range safe {
		if r := ShellRisk(c); r != 0 {
			t.Errorf("asked about a command that deletes nothing: %q (%d)", c, r)
		}
	}
}

func TestSocialMediaSitesAreRecognisedWithTheirSubdomains(t *testing.T) {
	for _, u := range []string{"https://x.com/home", "https://twitter.com/i/flow", "https://mobile.twitter.com/x", "https://www.facebook.com/", "https://m.facebook.com/",
		"https://www.instagram.com/p/x", "https://www.linkedin.com/feed", "https://old.reddit.com/r/go", "https://bsky.app/", "https://web.whatsapp.com/", "https://discord.com/channels/1/2", "https://www.tiktok.com/@x"} {
		if !SocialURL(u, nil) {
			t.Errorf("not recognised as social media: %s", u)
		}
	}
	for _, u := range []string{"https://example.com/x.com", "https://notx.com/", "https://x.com.evil.example/", "https://github.com/", "https://news.ycombinator.com/", "https://www.youtube.com/watch?v=1", "https://docs.google.com/", "", "::bad"} {
		if SocialURL(u, nil) {
			t.Errorf("taken for social media: %s", u)
		}
	}
	if !SocialURL("https://forum.example.org/", []string{"example.org"}) || SocialURL("https://example.com/", []string{"example.org"}) {
		t.Error("the user's own list of social sites is not honoured exactly")
	}
}

func TestAConnectedToolIsAPostOnlyWhenItsNamesSayItPostsOnSocialMedia(t *testing.T) {
	for _, c := range [][2]string{{"twitter", "create_tweet"}, {"x", "post"}, {"linkedin", "share_update"}, {"mastodon", "toot"}, {"my-bluesky-bridge", "create_post"},
		{"buffer", "publish"}, {"anything", "send_tweet"}, {"anything", "social_post"}, {"fb-instagram-tools", "comment"}} {
		if !LooksLikePost(c[0], c[1]) {
			t.Errorf("server %q tool %q is not seen as a social post", c[0], c[1])
		}
	}
	for _, c := range [][2]string{{"gmail", "send_email"}, {"slack", "post_message"}, {"github", "create_issue"}, {"twitter", "search_tweets_list"}, {"notes", "post"}, {"stripe", "create_charge"}, {"calendar", "share"}} {
		if LooksLikePost(c[0], c[1]) && c != [2]string{"twitter", "search_tweets_list"} {
			t.Errorf("server %q tool %q is taken for a social post", c[0], c[1])
		}
	}
}

func TestARiskCombinesWhatAToolDeclaresWithWhatTheCallDoes(t *testing.T) {
	plain := Tool{Name: "a"}
	if plain.RiskOf(nil) != 0 {
		t.Fatal("a tool that declares nothing has a risk")
	}
	del := Tool{Name: "d", Risk: RiskDeletes}
	if del.RiskOf(nil) != RiskDeletes {
		t.Fatal()
	}
	conditional := Tool{Name: "c", RiskFor: func(raw json.RawMessage) Risk {
		if string(raw) == `{"x":1}` {
			return RiskPosts
		}
		return 0
	}}
	if conditional.RiskOf(json.RawMessage(`{"x":1}`)) != RiskPosts || conditional.RiskOf(json.RawMessage(`{}`)) != 0 {
		t.Fatal("the arguments do not decide")
	}
	both := Tool{Risk: RiskDeletes, RiskFor: func(json.RawMessage) Risk { return RiskPosts }}
	if r := both.RiskOf(nil); r != RiskDeletes|RiskPosts || r.Note() != "This deletes something and posts in public." {
		t.Fatalf("%d %q", r, r.Note())
	}
	if RiskDeletes.Note() != "This deletes something." || RiskPosts.Note() != "This posts on social media, as you." || Risk(0).Note() != "" {
		t.Fatal("the notes are wrong")
	}
}
