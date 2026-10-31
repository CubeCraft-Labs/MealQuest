-- 0022_site_name_from_page: recipes.site_name now holds only the name a
-- recipe page gives its site (its publisher's name or og:site_name), and is
-- null when the page names none; a client is credited with the link's host
-- only when dinners.SourceOf finds the link safe to show. Before this, a
-- page naming no site was stored under the host of the URL it was served
-- from, which may carry a personal token, and nothing recorded which a
-- stored name was. A stored name that could be a host is cleared here, so
-- every stored name is one a page gave: one with no space (a host has
-- none) that is dotted or holds a colon — a host name or an IP address —
-- or that appears in the recipe's own link, as a dotless host does. This
-- clears every name the read rule before it withheld, and more: a site
-- that names itself as a domain, such as "Food.com", or by a word its link
-- spells, such as "Delish" on delish.com, loses its stored name until its
-- recipe is fetched again, and is credited by its link's host meanwhile
-- when the link is safe.
--
-- Idea job results stored before sources were made safe hold the grounded
-- recipe's site name and link as they were stored, and are cleared the
-- same way: an idea's stored site name is shown as it stands. The ideas
-- array is rebuilt from a plain scan of json_each, which yields its
-- elements in array order, so the ideas keep their order (sqlc's parser
-- refuses an ORDER BY inside json_group_array).

UPDATE recipes SET site_name = NULL
WHERE instr(site_name, ' ') = 0
	AND (site_name GLOB '*[.:]*' OR instr(lower(source_url), lower(site_name)) > 0);

UPDATE jobs SET result = json_set(result, '$.ideas', (
	SELECT json_group_array(
		CASE
			WHEN instr(json_extract(idea.value, '$.site_name'), ' ') = 0
				AND (json_extract(idea.value, '$.site_name') GLOB '*[.:]*'
					OR instr(lower(json_extract(idea.value, '$.source_url')), lower(json_extract(idea.value, '$.site_name'))) > 0)
			THEN json_set(idea.value, '$.site_name', json('null'))
			ELSE json(idea.value)
		END
	)
	FROM json_each(jobs.result, '$.ideas') AS idea
))
WHERE kind IN ('ideas_from_ingredients', 'quick_ideas')
	AND json_type(result, '$.ideas') = 'array';
