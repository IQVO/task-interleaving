Feature: Consumer rules of the task pool and the layout copy
  Every consumed message is claimed by its CloudEvents id first (per consumer): a
  repeat is a duplicate whatever it carries and whatever happened in between. A
  payload that can never be applied is an invalid event, dead-lettered at once.
  Missing local data is not an error: the consumer records what it can and ignores
  the rest. A late registration cannot undo a decommission, a decommissioned
  location keeps its geometry for origins and geometry is stored even before its
  location is registered.

  Background:
    Given site "SITE-1" has these locations:
      | code | x | y | z |
      | L0   | 0 | 0 | 0 |
      | A    | 3 | 0 | 0 |
      | B    | 3 | 4 | 0 |
      | E    | 0 | 3 | 0 |
    And site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    And task "T0" of type "PICK" is available at site "SITE-1" from "L0" to "L0"
    And station "STN-1" claims task "T0"
    And task "T-1" of type "PICK" is available at site "SITE-1" from "A" to "A"
    And task "T-2" of type "PICK" is available at site "SITE-1" from "B" to "B"

  Scenario Outline: A consumed message that lacks a required field can never be applied
    When a <event> event arrives for <fields>
    Then the command fails with "invalid event"

    Examples: TaskAvailable needs task_id, task_type, a valid site_id and from_location
      | event                      | fields                                                  |
      | TaskAvailable              | task "" of type "PICK" at site "SITE-1" from "A"        |
      | TaskAvailable              | task "T-9" of type "" at site "SITE-1" from "A"         |
      | TaskAvailable              | task "T-9" of type "PICK" at site "" from "A"           |
      | TaskAvailable              | task "T-9" of type "PICK" at site "SITE/1" from "A"     |
      | TaskAvailable              | task "T-9" of type "PICK" at site "SITE-1" from ""      |

    Examples: TaskClaimed needs task_id and a valid station_id, TaskCompleted needs task_id
      | event                      | fields                                                  |
      | TaskClaimed                | task "" by station "STN-1"                              |
      | TaskClaimed                | task "T-1" by station ""                                |
      | TaskClaimed                | task "T-1" by station "bad id"                          |
      | TaskCompleted              | task ""                                                 |

    Examples: the layout events need their keys
      | event                      | fields                                                  |
      | ZoneRegistered             | zone "" of site "SITE-1"                                |
      | ZoneRegistered             | zone "z-1" of site ""                                   |
      | LocationSlotRegistered     | location "" in zone "z-SITE-1"                          |
      | LocationSlotRegistered     | location "A2" in zone ""                                |
      | LocationSlotDecommissioned | location ""                                             |
      | LocationGeometryUpdated    | location "" at "1", "1", "1"                            |

  Scenario Outline: A geometry with a coordinate that is not a finite number can never be applied
    When a LocationGeometryUpdated event arrives for location "A" at "<x>", "<y>", "<z>"
    Then the command fails with "invalid event"

    Examples:
      | x    | y    | z    |
      | NaN  | 1    | 1    |
      | 1    | +Inf | 1    |
      | 1    | 1    | -Inf |

  Scenario Outline: A message without a CloudEvents id can never be applied
    When a <event> event arrives without a CloudEvents id
    Then the command fails with "invalid event"

    Examples:
      | event                      |
      | TaskAvailable              |
      | TaskClaimed                |
      | TaskCompleted              |
      | ZoneRegistered             |
      | LocationSlotRegistered     |
      | LocationSlotDecommissioned |
      | LocationGeometryUpdated    |

  Scenario: A CloudEvents id already claimed is a duplicate whatever the second payload carries
    When a TaskAvailable event with id "evt-A" announces task "T-9" at site "SITE-1" from "B"
    And a TaskAvailable event with id "evt-A" announces task "T-8" at site "SITE-1" from "A"
    Then the consumer outcome is "duplicate"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-1,T-2,T-9"

  Scenario: A redelivered announcement cannot put a claimed task back into the pool
    When a TaskAvailable event with id "evt-A" announces task "T-9" at site "SITE-1" from "A"
    And station "STN-2" claims task "T-9"
    And a TaskAvailable event with id "evt-A" announces task "T-9" at site "SITE-1" from "A"
    Then the consumer outcome is "duplicate"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-1,T-2"

  Scenario: The two consumers claim ids separately, one id can be used by each
    When a ZoneRegistered event with id "evt-shared" registers zone "z-9" of site "SITE-9"
    Then the consumer outcome is "applied"
    When a TaskAvailable event with id "evt-shared" announces task "T-9" at site "SITE-1" from "A"
    Then the consumer outcome is "applied"

  Scenario: A task that re-enters the pool forgets its old claimer
    Given station "STN-2" claims task "T-1"
    And task "T-1" of type "PICK" is available at site "SITE-1" from "A" to "A"
    When task "T-1" is completed
    Then the consumer outcome is "ignored"
    And station "STN-2" is unknown

  Scenario: The completer is the station the event names, else the claimer
    When station "STN-3" completes task "T-1"
    Then the consumer outcome is "applied"
    And station "STN-3" stands at "A"
    And station "STN-3" has an active hint listing "T-2"
    Given station "STN-2" claims task "T-2"
    When 10 seconds pass
    And station "STN-4" completes task "T-2"
    Then station "STN-4" stands at "B"
    And station "STN-2" is unknown

  Scenario: A completion by a station id that is not a valid token is ignored
    When station "bad id" completes task "T-1"
    Then the consumer outcome is "ignored"
    And 0 hints were issued

  Scenario: A task whose end location is blank cannot move its station
    Given task "T7" of type "PICK" is available at site "SITE-1" from "A" to "   "
    And station "STN-1" claims task "T7"
    When task "T7" is completed
    Then the consumer outcome is "ignored"
    And station "STN-1" is unknown

  Scenario: A task completed twice moves nothing the second time, even at a later instant
    Given task "T0" is completed
    And 10 seconds pass
    When task "T0" is completed
    Then the consumer outcome is "stale"
    And 1 hint was issued
    And hint number 1 is "Active"

  Scenario: A claim of a task that is already completed is stale and consumes nothing
    Given task "T0" is completed
    And station "STN-2" claims task "T-1"
    And task "T-1" is completed
    When station "STN-1" claims task "T-1"
    Then the consumer outcome is "stale"
    And hint number 1 is "Active"

  Scenario: A late registration cannot undo a decommission
    Given task "T-retired" of type "PICK" is available at site "SITE-1" from "E" to "E"
    And the location "E" is decommissioned
    And the location "E" of site "SITE-1" is registered without geometry
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-1,T-2"

  Scenario: A decommissioned location keeps its geometry as an origin
    Given task "T9" of type "PICK" is available at site "SITE-1" from "L0" to "E"
    And station "STN-1" claims task "T9"
    And the location "E" is decommissioned
    When task "T9" is completed
    Then station "STN-1" stands at "E"
    And station "STN-1" has an active hint listing "T-2,T-1"
    And candidate 1 of the active hint of station "STN-1" is "T-2" with detour 4 m

  Scenario: Geometry is stored before its location is registered and makes the task a candidate
    Given the geometry of "NEW" is updated to 5, 0, 0
    And task "T-new" of type "PICK" is available at site "SITE-1" from "NEW" to "NEW"
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-1,T-new,T-2"
    And candidate 2 of the active hint of station "STN-1" is "T-new" with detour 5 m

  Scenario: A newer geometry replaces the old point of a location
    Given the geometry of "A" is updated to 20, 0, 0
    When task "T0" is completed
    Then station "STN-1" has an active hint listing "T-2,T-1"
    And candidate 2 of the active hint of station "STN-1" is "T-1" with detour 20 m
