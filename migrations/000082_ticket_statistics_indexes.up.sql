ALTER TABLE `bp_ticket`
  ADD KEY `idx_bp_ticket_created_at` (`created_at`),
  ADD KEY `idx_bp_ticket_resolved_at` (`resolved_at`);
