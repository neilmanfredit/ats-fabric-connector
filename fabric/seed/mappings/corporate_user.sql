-- See fabric/seed/MAPPING.md. Soft-deleted rows are included deliberately
-- (Bronze never removes rows; build brief section 6.5). `departments` is a
-- many-to-many association in the API and is not seeded (rule 3 in MAPPING.md).
SELECT
    Id                  AS id,
    Name                AS name,
    Email               AS email,
    IsDeleted           AS isDeleted,
    DateLastModified    AS dateLastModified
FROM CorporateUser;
