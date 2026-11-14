Feature: Simulation rules from the use-case catalogue
  The simulation needs an origin with known geometry AND a site: the origin is the
  fromLocation, else the station's recorded position, measured on the geometry the
  layout copy holds now. A decommissioned location is still a valid origin. It runs
  under the policy version in force and never writes.

  Background:
    Given site "SITE-1" has these locations:
      | code | x | y | z |
      | L0   | 0 | 0 | 0 |
      | A    | 3 | 0 | 0 |
      | B    | 3 | 4 | 0 |
      | E    | 0 | 3 | 0 |
    And site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    And task "T-1" of type "PICK" is available at site "SITE-1" from "A" to "A"
    And task "T-2" of type "PICK" is available at site "SITE-1" from "B" to "B"

  Scenario: Geometry that reaches the layout copy after the completion makes the recorded position usable
    Given the location "N" of site "SITE-1" is registered without geometry
    And task "T9" of type "PICK" is available at site "SITE-1" from "L0" to "N"
    And station "STN-1" claims task "T9"
    And task "T9" is completed
    When I simulate for station "STN-1"
    Then the command fails with "no known geometry"
    When the geometry of "N" is updated to 0, 3, 0
    And I simulate for station "STN-1"
    Then the simulation lists "T-2,T-1"
    And the simulation is for site "SITE-1" from "N" under policy version 1

  Scenario: A location with geometry but no zone has no site, so it cannot be an origin
    Given the geometry of "ORPHAN" is updated to 1, 0, 0
    When I simulate for station "STN-9" from "ORPHAN"
    Then the command fails with "no known geometry"

  Scenario: A decommissioned location is still a valid origin
    Given the location "E" is decommissioned
    When I simulate for station "STN-9" from "E"
    Then the simulation lists "T-2,T-1"
    And the simulated candidate 1 is "T-2" with detour 4 m
    And the simulated candidate 2 is "T-1" with detour 6 m

  Scenario: A simulation runs under the policy version in force
    Given I declare the policy of site "SITE-1" expecting version 1 with max detour 25 m, up to 1 candidates and a 30 s hint ttl for "PICK"
    When I simulate for station "STN-9" from "L0"
    Then the simulation lists "T-1"
    And the simulation is for site "SITE-1" from "L0" under policy version 2
    And the published events are "InterleavingPolicyDeclared,InterleavingPolicyDeclared"
