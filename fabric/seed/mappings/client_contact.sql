-- See fabric/seed/MAPPING.md. Soft-deleted rows are included deliberately
-- (Bronze never removes rows; build brief section 6.5).
-- ClientCorporationID column name to confirm via runbook (section 6.2).
SELECT
    Id                    AS id,
    FirstName             AS firstName,
    LastName              AS lastName,
    Email                 AS email,
    Status                AS status,
    IsDeleted             AS isDeleted,
    DateAdded             AS dateAdded,
    DateLastModified      AS dateLastModified,
    ClientCorporationID   AS clientCorporation,
    OwnerID               AS owner
FROM ClientContact;
