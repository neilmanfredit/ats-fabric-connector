-- See fabric/seed/MAPPING.md. Soft-deleted rows are included deliberately
-- (Bronze never removes rows; build brief section 6.5).
-- ClientCorporationID and NumOpenings column names to confirm via runbook.
SELECT
    Id                    AS id,
    Title                 AS title,
    Status                AS status,
    IsDeleted             AS isDeleted,
    IsOpen                AS isOpen,
    DateAdded             AS dateAdded,
    DateLastModified      AS dateLastModified,
    DateEnd               AS dateEnd,
    ClientCorporationID   AS clientCorporation,
    OwnerID               AS owner,
    EmploymentType        AS employmentType,
    NumOpenings           AS numOpenings
FROM JobOrder;
