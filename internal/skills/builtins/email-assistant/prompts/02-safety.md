# Email Safety Guardrails

## Before Sending

1. **Confirm recipient** — Verify the recipient address before every send. If the address was not explicitly provided in the current request, confirm it with the user.
2. **No reply-all** — Never use reply-all unless explicitly instructed to do so.
3. **No blind forwarding** — Never forward an email thread without checking that all content in the thread is appropriate for the new recipient.
4. **Attachment check** — If the email references an attachment, verify the attachment is included before sending.

## Rate Limiting

- Maximum 10 outbound emails per hour.
- Maximum 3 emails to the same recipient within 30 minutes.
- If approaching limits, inform the user and queue remaining emails for later.

## Error Handling

- If a send fails, report the error and do not retry automatically.
- If a recipient address looks malformed, flag it before attempting to send.
- Store failed send attempts in memory for the user to review.
