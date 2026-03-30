package routes

import (
	"net/http"
	"qa-extension-backend/agent"

	"github.com/gin-gonic/gin"
)

func CreateCustomCommand(c *gin.Context) {
	sessionID, _ := c.Get("session_id")
	userID, ok := sessionID.(string)
	if !ok || userID == "" {
		userID = "default"
	}

	var req struct {
		Name           string `json:"name" binding:"required"`
		Description    string `json:"description"`
		ToolName       string `json:"tool_name" binding:"required"`
		PromptTemplate string `json:"prompt_template" binding:"required"`
	}

	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	cmd := &agent.CustomCommand{
		UserID:         userID,
		Name:           req.Name,
		Description:    req.Description,
		ToolName:       req.ToolName,
		PromptTemplate: req.PromptTemplate,
	}

	if err := agent.SaveCustomCommand(c.Request.Context(), cmd); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, cmd)
}

func ListCustomCommands(c *gin.Context) {
	sessionID, _ := c.Get("session_id")
	userID, ok := sessionID.(string)
	if !ok || userID == "" {
		userID = "default"
	}

	commands, err := agent.GetUserCustomCommands(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if commands == nil {
		commands = []*agent.CustomCommand{}
	}

	c.JSON(http.StatusOK, gin.H{"commands": commands})
}

func DeleteCustomCommand(c *gin.Context) {
	sessionID, _ := c.Get("session_id")
	userID, ok := sessionID.(string)
	if !ok || userID == "" {
		userID = "default"
	}

	commandID := c.Param("id")
	if commandID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "command id is required"})
		return
	}

	if err := agent.DeleteCustomCommand(c.Request.Context(), userID, commandID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Command deleted"})
}
