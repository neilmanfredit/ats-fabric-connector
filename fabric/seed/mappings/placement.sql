-- See fabric/seed/MAPPING.md. Soft-deleted rows are included deliberately
-- (Bronze never removes rows; build brief section 6.5).
-- Foreign-key column names, and whether Placement is effective-dated in
-- this tenant, are to confirm via runbook (section 6.2.4).
SELECT
    Id                    AS id,
    Status                AS status,
    IsDeleted             AS isDeleted,
    DateAdded             AS dateAdded,
    DateLastModified      AS dateLastModified,
    DateBegin             AS dateBegin,
    DateEnd               AS dateEnd,
    CandidateID           AS candidate,
    JobOrderID            AS jobOrder,
    ClientCorporationID   AS clientCorporation,
    EmploymentType        AS employmentType
FROM Placement;
