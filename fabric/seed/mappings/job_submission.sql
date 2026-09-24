-- See fabric/seed/MAPPING.md. Soft-deleted rows are included deliberately
-- (Bronze never removes rows; build brief section 6.5).
SELECT
    Id                  AS id,
    Status              AS status,
    IsDeleted           AS isDeleted,
    DateAdded           AS dateAdded,
    DateLastModified    AS dateLastModified,
    CandidateID         AS candidate,
    JobOrderID          AS jobOrder,
    SendingUserID       AS sendingUser
FROM JobSubmission;
