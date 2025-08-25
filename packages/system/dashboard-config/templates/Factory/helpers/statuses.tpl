{{- define "incloud-web-resources.factory.statuses.deployment" -}}
- type: StatusText
  data:
    id: header-status
    # 1) Collect all possible Deployment conditions
    values:
      - "{reqsJsonPath[0]['.status.conditions[*].reason']['-']}"

    # 2) Criteria: positive / negative; neutral goes to fallback
    criteriaSuccess: equals
    valueToCompareSuccess:
      # Positive reasons
      - "MinimumReplicasAvailable"     # Available: all replicas are healthy
      - "NewReplicaSetAvailable"       # Progressing: new RS serves traffic
      - "ReplicaSetUpdated"            # Progressing: RS is updated/synced
      - "Complete"                     # Update completed successfully

    criteriaError: equals
    valueToCompareError:
      # Negative reasons
      - "DeploymentReplicaFailure"     # General replica failure
      - "FailedCreate"                 # Failed to create Pod/RS
      - "FailedDelete"                 # Failed to delete resource
      - "FailedScaleUp"                # Failed to scale up
      - "FailedScaleDown"              # Failed to scale down

    # 3) Texts to display
    successText:  "Available"
    errorText:    "Error"
    fallbackText: "Progressing"

    # Notes on neutral/fallback cases:
    # - ReplicaSetUpdated        → neutral/positive (update in progress)
    # - ScalingReplicaSet        → neutral (normal scale up/down)
    # - Paused / DeploymentPaused→ neutral (manually paused by admin)
    # - NewReplicaSetCreated     → neutral (new RS created, not yet serving)
    # - FoundNewReplicaSet       → neutral (RS found, syncing)
    # - MinimumReplicasUnavailable → neutral (some replicas not ready yet)
    # - ProgressDeadlineExceeded → error-like, stuck in progress
{{- end -}}

{{- define "incloud-web-resources.factory.statuses.pod" -}}
- type: StatusText
  data:
    id: pod-status

    # --- Collected values from Pod status -----------------------------------
    values:
      # Init containers
      - "{reqsJsonPath[0]['.status.initContainerStatuses[*].state.waiting.reason']}"
      - "{reqsJsonPath[0]['.status.initContainerStatuses[*].state.terminated.reason']}"
      - "{reqsJsonPath[0]['.status.initContainerStatuses[*].lastState.terminated.reason']}"

      # Main containers
      - "{reqsJsonPath[0]['.status.containerStatuses[*].state.waiting.reason']}"
      - "{reqsJsonPath[0]['.status.containerStatuses[*].state.terminated.reason']}"
      - "{reqsJsonPath[0]['.status.containerStatuses[*].lastState.terminated.reason']}"

      # Pod phase and general reason
      - "{reqsJsonPath[0]['.status.phase']}"
      - "{reqsJsonPath[0]['.status.reason']}"

      # Condition reasons (PodScheduled / Initialized / ContainersReady / Ready)
      - "{reqsJsonPath[0]['.status.conditions[*].reason']}"

    # --- Success criteria ---------------------------------------------------
    criteriaSuccess: notEquals
    stategySuccess: every
    valueToCompareSuccess:
      # Graceful or expected state transitions
      - "Preempted"
      - "Shutdown"
      - "NodeShutdown"
      - "DisruptionTarget"

      # Transitional states (may require timeout)
      - "Unschedulable"
      - "SchedulingGated"
      - "ContainersNotReady"
      - "ContainersNotInitialized"

      # Temporary failures
      - "BackOff"

      # Controlled shutdowns or benign errors
      - "PreStopHookError"
      - "KillError"
      - "ContainerStatusUnknown"

    # --- Error criteria -----------------------------------------------------
    criteriaError: equals
    strategyError: every
    valueToCompareError:
      # Pod-level fatal phases or errors
      - "Failed"
      - "Unknown"
      - "Evicted"
      - "NodeLost"
      - "UnexpectedAdmissionError"

      # Scheduler-related failures
      - "SchedulerError"
      - "FailedScheduling"

      # Container-level fatal errors
      - "CrashLoopBackOff"
      - "ImagePullBackOff"
      - "ErrImagePull"
      - "ErrImageNeverPull"
      - "InvalidImageName"
      - "ImageInspectError"
      - "CreateContainerConfigError"
      - "CreateContainerError"
      - "RunContainerError"
      - "StartError"
      - "PostStartHookError"
      - "ContainerCannotRun"
      - "OOMKilled"
      - "Error"
      - "DeadlineExceeded"
      - "CreatePodSandboxError"

    # --- Output text rendering ----------------------------------------------
    successText:  "{reqsJsonPath[0]['.status.phase']}"
    errorText:    "Error"
    fallbackText: "Progressing"
{{- end -}}

{{- define "incloud-web-resources.factory.statuses.node" -}}
- type: StatusText
  data:
    id: node-status

    # --- Collected values from Node status ----------------------------------
    values:
      # Node phase and conditions
      - "{reqsJsonPath[0]['.status.conditions[?(@.status=='True')].reason']['-']}"

    # --- Success criteria ---------------------------------------------------
    criteriaSuccess: equals
    stategySuccess: every
    valueToCompareSuccess:
      "KubeletReady"

    # --- Error criteria -----------------------------------------------------
    criteriaError: equals
    strategyError: every
    valueToCompareError:
      # Node condition failures
      - "KernelDeadlock"
      - "ReadonlyFilesystem"
      - "NetworkUnavailable"
      - "MemoryPressure"
      - "DiskPressure"
      - "PIDPressure"

    # --- Output text rendering ----------------------------------------------
    successText:  "Available"
    errorText:    "Unavailable"
    fallbackText: "Progressing"
{{- end -}}

{{- define "incloud-web-resources.factory.statuses.job" -}}
- type: StatusText
  data:
    id: header-status

    # --- Collected values from Job conditions -------------------------------
    values: 
      # Extracts the type of any condition where type == 'Complete' or 'Failed'
      - "{reqsJsonPath[0]['.status.conditions[?(@.type=='Complete' || @.type=='Failed')].type']['-']}"

    # --- Success criteria ---------------------------------------------------
    criteriaSuccess: equals
    stategySuccess: every
    valueToCompareSuccess:
      - "Complete"    # Job succeeded

    # --- Error criteria -----------------------------------------------------
    criteriaError: equals
    strategyError: every     # ← likely meant to be `strategyError`
    valueToCompareError:
      - "Failed"      # Job failed

    # --- Output text rendering ----------------------------------------------
    successText:  "Available"
    errorText:    "Unavailable"
    fallbackText: "Progressing"
{{- end -}}
