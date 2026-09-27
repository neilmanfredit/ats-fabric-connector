-- Seed mapping for the candidate table. Column names follow the API field
-- names allowlisted in fabric/runner/entities.yaml; see fabric/seed/MAPPING.md
-- for the "to confirm via runbook" notes on owner/source foreign keys.
SELECT
    Id                  AS id,
    FirstName           AS firstName,
    LastName            AS lastName,
    Email               AS email,
    Status              AS status,
    IsDeleted           AS isDeleted,
    DateAdded           AS dateAdded,
    DateLastModified    AS dateLastModified,
    OwnerID             AS owner,
    CandidateSource     AS candidateSource
FROM Candidate;
-- Soft-deleted rows are included deliberately: Bronze never removes rows
-- (build brief section 6.5), so excluding them here would create a
-- permanent gap for anything deleted before the seed watermark.
