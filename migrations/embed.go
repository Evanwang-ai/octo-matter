package migrations

import "embed"

//go:embed 001_init.sql 002_rename_to_matters.sql 003_permissions_upgrade.sql 004_digest_and_activities.sql 005_unify_timeline.sql 006_matter_source_msg_ids.sql 007_timeline_attachment_outputs.sql 008_matter_v2_engine.sql 009_brief_schedule_sources.sql 010_agent_cards.sql 011_agent_card_visibility.sql
var FS embed.FS
