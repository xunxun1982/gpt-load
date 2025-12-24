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

	if status.TaskType != services.TaskTypeKeyImport && status.TaskType != services.TaskTypeKeyDelete {
		return false
	}

	if s.DB.Dialector.Name() == "sqlite" {
		return true
	}

	return groupName != "" && status.GroupName == groupName
}
