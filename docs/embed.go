// Package docs embeds agent-facing documentation served by the service
// (GET /skill.md, GET /modes/:name) — same distribution pattern as
// octo-server's BotFather /v1/bot/skill.md: a bot that gets @'d can fetch
// its operating manual with one request against the service it is about to use.
package docs

import "embed"

//go:embed SKILL.md
var SkillMD []byte

//go:embed modes/*.md
var ModesFS embed.FS
