ALTER TABLE agents ADD COLUMN http_allowed_hosts TEXT DEFAULT '[]';
ALTER TABLE agents ADD COLUMN shell_allowed_commands TEXT DEFAULT '[]';
