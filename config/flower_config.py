"""
Flower Configuration for Ligand-X
Enables queue visualization and custom dashboard settings
"""

# ============================================================
# Queue Configuration
# ============================================================
# Display all queues in the dashboard
CELERY_QUEUES = [
    'qc',           # QC calculations (Quantum Chemistry)
    'gpu-short',    # GPU short jobs (MD, ABFE, RBFE, Boltz2 - fast)
    'gpu-long',     # GPU long jobs (MD, ABFE, RBFE, Boltz2 - long-running)
    'cpu',          # CPU batch docking
]

# ============================================================
# Dashboard Settings
# ============================================================
# Enable persistent storage of task history
FLOWER_PERSISTENT = True

# Task history retention (in seconds) - 7 days
FLOWER_MAX_TASKS = 10000

# ============================================================
# Worker Display Settings
# ============================================================
# Show worker pool information
FLOWER_SHOW_POOL = True

# ============================================================
# Task Display Settings
# ============================================================
# Show task arguments and results
FLOWER_TASK_TRACK_STARTED = True

# ============================================================
# UI Settings
# ============================================================
# Enable real-time updates
FLOWER_ENABLE_MMAP = True

# Refresh rate (in seconds)
FLOWER_REFRESH_INTERVAL = 5000

# ============================================================
# Logging
# ============================================================
# Log level
FLOWER_LOGGING = 'info'
