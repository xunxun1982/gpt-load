package handler

import "gpt-load/internal/services"

func (s *Server) shouldDegradeReadDuringTask(groupName string) bool {
	if s == nil || s.TaskService == nil || s.DB == nil {
		return false
	}

	status, err := s.TaskService.GetTaskStatus()
	if err != nil || status == nil || !status.IsRunning {
		return false
	}

	if status.TaskType != services.TaskTypeKeyImport && status.TaskType != services.TaskTypeKeyDelete && status.TaskType != services.TaskTypeKeyRestore {
		return false
	}

	// Only degrade reads for the group being operated on, not all groups
	// This prevents affecting other groups during large delete/import operations
	return groupName != "" && status.GroupName == groupName
}
