-- 0017_shopping_list_chat_send_unconfirmed: a send of a Supply run to
-- Google Chat is recorded as begun, in chat_send_unconfirmed_at, before it
-- is posted, and cleared when Google Chat's answer is known: with
-- sent_to_chat_at when it accepted the post, alone when it refused it. A
-- value left behind (Google Chat did not answer, the process stopped, or
-- recording the answer failed) means the post may or may not have been
-- made, so it is never posted again without resend. Existing rows have no
-- send under way.

ALTER TABLE shopping_lists ADD COLUMN chat_send_unconfirmed_at TEXT;
