-- Add inbound webhook config to agents
ALTER TABLE agents ADD COLUMN webhook_inbound TEXT;
