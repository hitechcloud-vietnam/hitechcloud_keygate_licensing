-- Where customers download the product (issue #36 item 5): a stable page
-- of the vendor's own, offered to email templates as {{.DownloadURL}}.
-- Empty means not set; the default templates then show no download link.
ALTER TABLE products ADD COLUMN IF NOT EXISTS download_url TEXT NOT NULL DEFAULT '';
